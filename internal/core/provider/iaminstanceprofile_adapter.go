// IAMInstanceProfile provider adapter.
//
// This file implements the provider.Adapter interface for AWS IAM (Instance Profile)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the IAMInstanceProfile Restate Virtual Object driver.
//
// Key scope: global (IAM is region-free).
// Key parts: instance profile name.
// IAM instance profiles are global; the key is the instance profile name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/iaminstanceprofile"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// IAMInstanceProfileAdapter implements provider.Adapter for IAMInstanceProfile (AWS IAM (Instance Profile)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type IAMInstanceProfileAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI iaminstanceprofile.IAMInstanceProfileAPI
	apiFactory        func(aws.Config) iaminstanceprofile.IAMInstanceProfileAPI
}

// NewIAMInstanceProfileAdapterWithAuth creates a production IAMInstanceProfile adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewIAMInstanceProfileAdapterWithAuth(auth authservice.AuthClient) *IAMInstanceProfileAdapter {
	return &IAMInstanceProfileAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) iaminstanceprofile.IAMInstanceProfileAPI {
			return iaminstanceprofile.NewIAMInstanceProfileAPI(awsclient.NewIAMClient(cfg))
		},
	}
}

// NewIAMInstanceProfileAdapterWithAPI creates a IAMInstanceProfile adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewIAMInstanceProfileAdapterWithAPI(api iaminstanceprofile.IAMInstanceProfileAPI) *IAMInstanceProfileAdapter {
	return &IAMInstanceProfileAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "IAMInstanceProfile" that maps template
// resource documents to this adapter in the provider registry.
func (a *IAMInstanceProfileAdapter) Kind() string {
	return iaminstanceprofile.ServiceName
}

// ServiceName returns the Restate Virtual Object service name for the
// IAMInstanceProfile driver. The orchestrator uses this to dispatch durable RPCs.
func (a *IAMInstanceProfileAdapter) ServiceName() string {
	return iaminstanceprofile.ServiceName
}

// Scope returns the key-scope strategy for IAMInstanceProfile resources,
// which controls how BuildKey assembles the canonical object key.
func (a *IAMInstanceProfileAdapter) Scope() KeyScope {
	return KeyScopeGlobal
}

// BuildKey derives the canonical Restate object key for a IAMInstanceProfile resource
// from the raw JSON resource document. The key is composed of instance profile name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *IAMInstanceProfileAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("instance profile name", spec.InstanceProfileName); err != nil {
		return "", err
	}
	return spec.InstanceProfileName, nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete IAMInstanceProfile spec struct expected by the driver.
func (a *IAMInstanceProfileAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the IAMInstanceProfile Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *IAMInstanceProfileAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[iaminstanceprofile.IAMInstanceProfileSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[iaminstanceprofile.IAMInstanceProfileSpec, iaminstanceprofile.IAMInstanceProfileOutputs](
		restate.Object[iaminstanceprofile.IAMInstanceProfileOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[iaminstanceprofile.IAMInstanceProfileOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

// Delete sends a durable Delete request to the IAMInstanceProfile Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *IAMInstanceProfileAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed IAMInstanceProfile driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *IAMInstanceProfileAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[iaminstanceprofile.IAMInstanceProfileOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"arn":                 out.Arn,
		"instanceProfileId":   out.InstanceProfileId,
		"instanceProfileName": out.InstanceProfileName,
	}, nil
}

// Plan compares the desired IAMInstanceProfile spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *IAMInstanceProfileAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[iaminstanceprofile.IAMInstanceProfileSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}

	type describePlanResult struct {
		State iaminstanceprofile.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeInstanceProfile(runCtx, desired.InstanceProfileName)
		if descErr != nil {
			if iaminstanceprofile.IsNotFound(descErr) {
				return describePlanResult{Found: false}, nil
			}
			return describePlanResult{}, restate.TerminalError(descErr, 500)
		}
		return describePlanResult{State: obs, Found: true}, nil
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

	rawDiffs := iaminstanceprofile.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}

	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

// BuildImportKey derives the canonical Restate object key for importing
// an existing IAMInstanceProfile resource by its region and provider-native ID.
func (a *IAMInstanceProfileAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return resourceID, nil
}

// Import adopts an existing IAMInstanceProfile resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *IAMInstanceProfileAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, iaminstanceprofile.IAMInstanceProfileOutputs](
		restate.Object[iaminstanceprofile.IAMInstanceProfileOutputs](ctx, a.ServiceName(), key, "Import"),
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

// decodeSpec unmarshals the raw JSON spec from a resource document into
// the typed IAMInstanceProfile spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *IAMInstanceProfileAdapter) decodeSpec(doc resourceDocument) (iaminstanceprofile.IAMInstanceProfileSpec, error) {
	var spec struct {
		Path     string            `json:"path"`
		RoleName string            `json:"roleName"`
		Tags     map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return iaminstanceprofile.IAMInstanceProfileSpec{}, fmt.Errorf("decode IAMInstanceProfile spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return iaminstanceprofile.IAMInstanceProfileSpec{}, fmt.Errorf("IAMInstanceProfile metadata.name is required")
	}
	if strings.TrimSpace(spec.RoleName) == "" {
		return iaminstanceprofile.IAMInstanceProfileSpec{}, fmt.Errorf("IAMInstanceProfile spec.roleName is required")
	}
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return iaminstanceprofile.IAMInstanceProfileSpec{
		Account:             "",
		Path:                spec.Path,
		InstanceProfileName: name,
		RoleName:            spec.RoleName,
		Tags:                spec.Tags,
	}, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *IAMInstanceProfileAdapter) planningAPI(ctx restate.Context, account string) (iaminstanceprofile.IAMInstanceProfileAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("IAMInstanceProfile adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAMInstanceProfile planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
