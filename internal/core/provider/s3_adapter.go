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

func (a *S3Adapter) Scope() KeyScope {
	return KeyScopeGlobal
}

// S3Adapter adapts generic resource documents to the strongly typed S3 bucket driver.
type S3Adapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI s3.S3API
	apiFactory        func(aws.Config) s3.S3API
}

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

func (a *S3Adapter) Kind() string {
	return s3.ServiceName
}

func (a *S3Adapter) ServiceName() string {
	return s3.ServiceName
}

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

func (a *S3Adapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

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

func (a *S3Adapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})

	return &deleteHandle{
		id:  fut.GetInvocationId(),
		raw: fut,
	}, nil
}

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

func (a *S3Adapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return resourceID, nil
}

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
