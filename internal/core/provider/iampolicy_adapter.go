// IAMPolicy provider adapter.
//
// This file implements the provider.Adapter interface for AWS IAM (Policy)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the IAMPolicy Restate Virtual Object driver.
//
// Key scope: global (IAM is region-free).
// Key parts: policy name (optionally with path prefix).
// IAM policies are global; the key is derived from the policy name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/iampolicy"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// IAMPolicyAdapter implements provider.Adapter for IAMPolicy (AWS IAM (Policy)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type IAMPolicyAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI iampolicy.IAMPolicyAPI
	apiFactory        func(aws.Config) iampolicy.IAMPolicyAPI
}

// NewIAMPolicyAdapterWithAuth creates a production IAMPolicy adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewIAMPolicyAdapterWithAuth(auth authservice.AuthClient) *IAMPolicyAdapter {
	return &IAMPolicyAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) iampolicy.IAMPolicyAPI {
			return iampolicy.NewIAMPolicyAPI(awsclient.NewIAMClient(cfg))
		},
	}
}

// NewIAMPolicyAdapterWithAPI creates a IAMPolicy adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewIAMPolicyAdapterWithAPI(api iampolicy.IAMPolicyAPI) *IAMPolicyAdapter {
	return &IAMPolicyAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "IAMPolicy" that maps template
// resource documents to this adapter in the provider registry.
func (a *IAMPolicyAdapter) Kind() string {
	return iampolicy.ServiceName
}

// ServiceName returns the Restate Virtual Object service name for the
// IAMPolicy driver. The orchestrator uses this to dispatch durable RPCs.
func (a *IAMPolicyAdapter) ServiceName() string {
	return iampolicy.ServiceName
}

// Scope returns the key-scope strategy for IAMPolicy resources,
// which controls how BuildKey assembles the canonical object key.
func (a *IAMPolicyAdapter) Scope() KeyScope {
	return KeyScopeGlobal
}

// BuildKey derives the canonical Restate object key for a IAMPolicy resource
// from the raw JSON resource document. The key is composed of policy name (optionally with path prefix),
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *IAMPolicyAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("policy name", spec.PolicyName); err != nil {
		return "", err
	}
	return spec.PolicyName, nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete IAMPolicy spec struct expected by the driver.
func (a *IAMPolicyAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the IAMPolicy Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *IAMPolicyAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[iampolicy.IAMPolicySpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[iampolicy.IAMPolicySpec, iampolicy.IAMPolicyOutputs](
		restate.Object[iampolicy.IAMPolicyOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[iampolicy.IAMPolicyOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the IAMPolicy Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *IAMPolicyAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed IAMPolicy driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *IAMPolicyAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[iampolicy.IAMPolicyOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"arn": out.Arn, "policyId": out.PolicyId, "policyName": out.PolicyName}, nil
}

// Plan compares the desired IAMPolicy spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *IAMPolicyAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[iampolicy.IAMPolicySpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}

	type describePlanResult struct {
		State iampolicy.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribePolicyByName(runCtx, desired.PolicyName, desired.Path)
		if descErr != nil {
			if iampolicy.IsNotFound(descErr) {
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

	rawDiffs := iampolicy.ComputeFieldDiffs(desired, result.State)
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
// an existing IAMPolicy resource by its region and provider-native ID.
func (a *IAMPolicyAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return resourceID, nil
}

// Import adopts an existing IAMPolicy resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *IAMPolicyAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, iampolicy.IAMPolicyOutputs](
		restate.Object[iampolicy.IAMPolicyOutputs](ctx, a.ServiceName(), key, "Import"),
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
// the typed IAMPolicy spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *IAMPolicyAdapter) decodeSpec(doc resourceDocument) (iampolicy.IAMPolicySpec, error) {
	var spec struct {
		Path           string            `json:"path"`
		PolicyDocument string            `json:"policyDocument"`
		Description    string            `json:"description"`
		Tags           map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return iampolicy.IAMPolicySpec{}, fmt.Errorf("decode IAMPolicy spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return iampolicy.IAMPolicySpec{}, fmt.Errorf("IAMPolicy metadata.name is required")
	}
	if strings.TrimSpace(spec.PolicyDocument) == "" {
		return iampolicy.IAMPolicySpec{}, fmt.Errorf("IAMPolicy spec.policyDocument is required")
	}
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return iampolicy.IAMPolicySpec{Path: spec.Path, PolicyName: name, PolicyDocument: spec.PolicyDocument, Description: spec.Description, Tags: spec.Tags}, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *IAMPolicyAdapter) planningAPI(ctx restate.Context, account string) (iampolicy.IAMPolicyAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("IAMPolicy adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAMPolicy planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
