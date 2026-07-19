// S3Bucket provider adapter — descriptor for the GenericAdapter.
//
// Key scope: global (bucket names are globally unique).
// Key parts: bucket name alone.
// Buckets are globally unique so the key is just the bucket name with no
// region prefix.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/s3"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// S3Adapter is the descriptor-driven adapter for S3Bucket, extended with
// per-kind default timeouts, a custom live Observe,
// and a data-source Lookup. The Observe and Lookup paths need a planning API
// client, so the adapter keeps the auth/staticPlanningAPI/apiFactory wiring.
type S3Adapter struct {
	*GenericAdapter[s3.S3BucketSpec, s3.S3BucketOutputs, s3.ObservedState]
	auth              authservice.AuthClient
	staticPlanningAPI s3.S3API
	apiFactory        func(aws.Config) s3.S3API
}

func s3Descriptor() GenericDescriptor[s3.S3BucketSpec, s3.S3BucketOutputs, s3.ObservedState] {
	return GenericDescriptor[s3.S3BucketSpec, s3.S3BucketOutputs, s3.ObservedState]{
		Kind:  s3.ServiceName,
		Scope: KeyScopeGlobal,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (s3.S3BucketSpec, error) {
			var spec struct {
				Region     string            `json:"region"`
				Versioning bool              `json:"versioning"`
				ACL        string            `json:"acl"`
				Encryption s3.EncryptionSpec `json:"encryption"`
				Tags       map[string]string `json:"tags"`
			}
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return s3.S3BucketSpec{}, fmt.Errorf("decode S3Bucket spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return s3.S3BucketSpec{}, fmt.Errorf("S3Bucket metadata.name is required")
			}
			if strings.TrimSpace(spec.Region) == "" {
				return s3.S3BucketSpec{}, fmt.Errorf("S3Bucket spec.region is required")
			}
			return s3.S3BucketSpec{
				BucketName: name,
				Account:    "",
				Region:     spec.Region,
				Versioning: spec.Versioning,
				Encryption: spec.Encryption,
				ACL:        spec.ACL,
				Tags:       spec.Tags,
			}, nil
		},

		KeyFromSpec: func(spec s3.S3BucketSpec, _ string) (string, error) {
			if err := ValidateKeyPart("bucket name", spec.BucketName); err != nil {
				return "", err
			}
			return spec.BucketName, nil
		},

		ImportKey: func(_, resourceID string) (string, error) {
			if err := ValidateKeyPart("resource ID", resourceID); err != nil {
				return "", err
			}
			return resourceID, nil
		},

		PrepareSpec: func(spec s3.S3BucketSpec, _ string, account string) s3.S3BucketSpec {
			spec.Account = account
			return spec
		},

		NormalizeOutputs: func(out s3.S3BucketOutputs) map[string]any {
			return map[string]any{
				"arn":        out.ARN,
				"bucketName": out.BucketName,
				"region":     out.Region,
				"domainName": out.DomainName,
			}
		},

		PlanIdentity: storedPlanIdentity[s3.S3BucketSpec](func(out s3.S3BucketOutputs) string { return out.BucketName }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[s3.S3BucketSpec, s3.S3BucketOutputs, s3.ObservedState] {
			return s3Probe(s3.NewS3API(awsclient.NewS3Client(cfg)))
		},

		DiffFields: func(desired s3.S3BucketSpec, observed s3.ObservedState, _ s3.S3BucketOutputs) []types.FieldDiff {
			rawDiffs := s3.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// s3Probe adapts the driver API to the generic plan probe shape.
func s3Probe(api s3.S3API) PlanProbeFunc[s3.S3BucketSpec, s3.S3BucketOutputs, s3.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[s3.S3BucketSpec, s3.S3BucketOutputs]) (s3.ObservedState, bool, error) {
		bucketName := input.Identity
		obs, err := api.DescribeBucket(runCtx, bucketName)
		if err != nil {
			if s3.IsNotFound(err) {
				return s3.ObservedState{}, false, nil
			}
			return s3.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewS3AdapterWithAuth builds the production adapter; plan-time credentials
// are resolved through the Auth Service. The apiFactory closure creates a real
// AWS API client from the resolved aws.Config for Observe/Lookup calls.
func NewS3AdapterWithAuth(auth authservice.AuthClient) *S3Adapter {
	return &S3Adapter{
		GenericAdapter: NewGenericAdapter(s3Descriptor(), auth),
		auth:           auth,
		apiFactory: func(cfg aws.Config) s3.S3API {
			return s3.NewS3API(awsclient.NewS3Client(cfg))
		},
	}
}

// NewS3AdapterWithAPI injects a fixed S3 planning API used for both the plan
// probe and the Observe/Lookup paths. This is primarily useful in tests.
func NewS3AdapterWithAPI(api s3.S3API) *S3Adapter {
	return &S3Adapter{
		GenericAdapter:    NewGenericAdapterWithProbe(s3Descriptor(), s3Probe(api)),
		staticPlanningAPI: api,
	}
}

// DefaultTimeouts provides a longer delete timeout for bucket deletion.
func (a *S3Adapter) DefaultTimeouts() types.ResourceTimeouts {
	return types.ResourceTimeouts{Delete: "10m"}
}

// Observe performs a lightweight live check against AWS to determine whether
// the S3 bucket exists and matches the desired spec. This implements the
// Observer interface for the observe-before-act pattern.
func (a *S3Adapter) Observe(ctx restate.Context, key string, account string, spec any) (ObserveResult, error) {
	desired, err := castSpec[s3.S3BucketSpec](spec)
	if err != nil {
		return ObserveResult{}, err
	}
	api, err := a.planningAPI(ctx, account)
	if err != nil {
		return ObserveResult{}, err
	}
	type describeResult struct {
		State s3.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(rc restate.RunContext) (describeResult, error) {
		obs, descErr := api.DescribeBucket(rc, desired.BucketName)
		if descErr != nil {
			if s3.IsNotFound(descErr) {
				return describeResult{Found: false}, nil
			}
			return describeResult{}, descErr
		}
		return describeResult{State: obs, Found: true}, nil
	})
	if err != nil {
		return ObserveResult{}, err
	}
	if !result.Found {
		return ObserveResult{Exists: false}, nil
	}
	upToDate := !s3.HasDrift(desired, result.State)
	outputs, _ := a.NormalizeOutputs(s3.S3BucketOutputs{
		ARN:        fmt.Sprintf("arn:aws:s3:::%s", desired.BucketName),
		BucketName: desired.BucketName,
		Region:     desired.Region,
		DomainName: fmt.Sprintf("%s.s3.%s.amazonaws.com", desired.BucketName, desired.Region),
	})
	return ObserveResult{Exists: true, UpToDate: upToDate, Outputs: outputs}, nil
}

// Lookup performs a read-only data-source query for an existing S3Bucket
// resource, matching by ID, name, or tags. This is used by template data
// source blocks to resolve references to pre-existing infrastructure.
func (a *S3Adapter) Lookup(ctx restate.Context, account string, filter LookupFilter) (map[string]any, error) {
	api, err := a.planningAPI(ctx, account)
	if err != nil {
		return nil, restate.TerminalError(err, 500)
	}
	observed, err := restate.Run(ctx, func(runCtx restate.RunContext) (s3.ObservedState, error) {
		obs, runErr := lookupS3Bucket(runCtx, api, filter)
		if runErr != nil {
			return obs, classifyLookupError(runErr, s3.IsNotFound)
		}
		return obs, nil
	})
	if err != nil {
		return nil, err
	}
	if !matchesS3Filter(observed, filter) {
		return nil, restate.TerminalError(fmt.Errorf("data source lookup: no S3Bucket found matching filter"), 404)
	}
	outputs, err := a.NormalizeOutputs(s3.S3BucketOutputs{
		ARN:        fmt.Sprintf("arn:aws:s3:::%s", observed.BucketName),
		BucketName: observed.BucketName,
		Region:     observed.Region,
		DomainName: bucketDomainName(observed.BucketName, observed.Region),
	})
	if err != nil {
		return nil, restate.TerminalError(err, 500)
	}
	return outputs, nil
}

// planningAPI returns the AWS API client used for Observe/Lookup (read-only)
// operations. In production it resolves credentials for the given account via
// the auth client and creates a fresh API. In tests it returns the
// staticPlanningAPI.
func (a *S3Adapter) planningAPI(ctx restate.Context, account string) (s3.S3API, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("S3 adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve S3 planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}

func lookupS3Bucket(ctx restate.RunContext, api s3.S3API, filter LookupFilter) (s3.ObservedState, error) {
	name := strings.TrimSpace(filter.ID)
	if name == "" {
		name = strings.TrimSpace(filter.Name)
	}
	if name == "" && len(filter.Tag) > 0 {
		resolved, err := api.FindByTags(ctx, filter.Tag)
		if err != nil {
			return s3.ObservedState{}, err
		}
		name = strings.TrimSpace(resolved)
	}
	if name == "" {
		return s3.ObservedState{}, fmt.Errorf("not found")
	}
	return api.DescribeBucket(ctx, name)
}

func matchesS3Filter(observed s3.ObservedState, filter LookupFilter) bool {
	if strings.TrimSpace(filter.ID) != "" && observed.BucketName != strings.TrimSpace(filter.ID) {
		return false
	}
	if strings.TrimSpace(filter.Name) != "" && observed.BucketName != strings.TrimSpace(filter.Name) {
		return false
	}
	for key, value := range filter.Tag {
		if observed.Tags[key] != value {
			return false
		}
	}
	return true
}

func bucketDomainName(name, region string) string {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(region) == "" {
		return ""
	}
	return fmt.Sprintf("%s.s3.%s.amazonaws.com", name, region)
}
