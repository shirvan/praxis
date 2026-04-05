// S3Bucket provider adapter.
//
// This file implements the provider.Adapter interface for Amazon S3
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the S3Bucket Restate Virtual Object driver.
//
// Key scope: global (bucket names are globally unique).
// Key parts: bucket name alone.
// Buckets are globally unique so the key is just the bucket name with no region prefix.
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

// Scope returns the key-scope strategy for S3Bucket resources,
// which controls how BuildKey assembles the canonical object key.
func (a *S3Adapter) Scope() KeyScope {
	return KeyScopeGlobal
}

// S3Adapter implements provider.Adapter for S3Bucket (Amazon S3) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type S3Adapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI s3.S3API
	apiFactory        func(aws.Config) s3.S3API
}

// NewS3AdapterWithAuth creates a production S3Bucket adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewS3AdapterWithAuth(auth authservice.AuthClient) *S3Adapter {
	return &S3Adapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) s3.S3API {
			return s3.NewS3API(awsclient.NewS3Client(cfg))
		},
	}
}

// NewS3AdapterWithAPI injects a fixed S3 planning API. This is primarily useful
// in tests that do not need per-account planning behavior.
func NewS3AdapterWithAPI(api s3.S3API) *S3Adapter {
	return &S3Adapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "S3Bucket" that maps template
// resource documents to this adapter in the provider registry.
func (a *S3Adapter) Kind() string {
	return s3.ServiceName
}

// ServiceName returns the Restate Virtual Object service name for the
// S3Bucket driver. The orchestrator uses this to dispatch durable RPCs.
func (a *S3Adapter) ServiceName() string {
	return s3.ServiceName
}

// BuildKey derives the canonical Restate object key for a S3Bucket resource
// from the raw JSON resource document. The key is composed of bucket name alone,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *S3Adapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("bucket name", spec.BucketName); err != nil {
		return "", err
	}
	return spec.BucketName, nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete S3Bucket spec struct expected by the driver.
func (a *S3Adapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the S3Bucket Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *S3Adapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[s3.S3BucketSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[s3.S3BucketSpec, s3.S3BucketOutputs](
		restate.Object[s3.S3BucketOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[s3.S3BucketOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

// Delete sends a durable Delete request to the S3Bucket Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *S3Adapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})

	return &deleteHandle{
		id:  fut.GetInvocationId(),
		raw: fut,
	}, nil
}

// NormalizeOutputs converts the typed S3Bucket driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *S3Adapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[s3.S3BucketOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"arn":        out.ARN,
		"bucketName": out.BucketName,
		"region":     out.Region,
		"domainName": out.DomainName,
	}, nil
}

// Plan compares the desired S3Bucket spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *S3Adapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[s3.S3BucketSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}

	// describePlanResult packages the describe response so that "not found" is
	// a successful journal entry rather than a retried error.
	type describePlanResult struct {
		State s3.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		out, descErr := planningAPI.DescribeBucket(runCtx, desired.BucketName)
		if descErr != nil {
			if s3.IsNotFound(descErr) {
				return describePlanResult{Found: false}, nil
			}
			return describePlanResult{}, restate.TerminalError(descErr, 500)
		}
		return describePlanResult{State: out, Found: true}, nil
	})
	if err != nil {
		return "", nil, err
	}
	if !result.Found {
		fields, fieldErr := createFieldDiffsFromSpec(desired)
		if fieldErr != nil {
			return "", nil, fieldErr
		}
		return types.OpCreate, fields, nil
	}
	observed := result.State

	rawDiffs := s3.ComputeFieldDiffs(desired, observed)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}

	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{
			Path:     diff.Path,
			OldValue: diff.OldValue,
			NewValue: diff.NewValue,
		})
	}
	return types.OpUpdate, fields, nil
}

// BuildImportKey derives the canonical Restate object key for importing
// an existing S3Bucket resource by its region and provider-native ID.
func (a *S3Adapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return resourceID, nil
}

// Import adopts an existing S3Bucket resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *S3Adapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, s3.S3BucketOutputs](
		restate.Object[s3.S3BucketOutputs](ctx, a.ServiceName(), key, "Import"),
	).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
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

// decodeSpec unmarshals the raw JSON spec from a resource document into
// the typed S3Bucket spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *S3Adapter) decodeSpec(doc resourceDocument) (s3.S3BucketSpec, error) {
	var spec struct {
		Region     string            `json:"region"`
		Versioning bool              `json:"versioning"`
		ACL        string            `json:"acl"`
		Encryption s3.EncryptionSpec `json:"encryption"`
		Tags       map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return s3.S3BucketSpec{}, fmt.Errorf("decode S3Bucket spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
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
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
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
