// IAMGroup provider adapter.
//
// This file implements the provider.Adapter interface for AWS IAM (Group)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the IAMGroup Restate Virtual Object driver.
//
// Key scope: global (IAM is region-free).
// Key parts: group name.
// IAM groups are global; the key is the group name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/iamgroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// IAMGroupAdapter implements provider.Adapter for IAMGroup (AWS IAM (Group)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type IAMGroupAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI iamgroup.IAMGroupAPI
	apiFactory        func(aws.Config) iamgroup.IAMGroupAPI
}

// NewIAMGroupAdapterWithAuth creates a production IAMGroup adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewIAMGroupAdapterWithAuth(auth authservice.AuthClient) *IAMGroupAdapter {
	return &IAMGroupAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) iamgroup.IAMGroupAPI {
			return iamgroup.NewIAMGroupAPI(awsclient.NewIAMClient(cfg))
		},
	}
}

// NewIAMGroupAdapterWithAPI creates a IAMGroup adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewIAMGroupAdapterWithAPI(api iamgroup.IAMGroupAPI) *IAMGroupAdapter {
	return &IAMGroupAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "IAMGroup" that maps template
// resource documents to this adapter in the provider registry.
func (a *IAMGroupAdapter) Kind() string {
	return iamgroup.ServiceName
}

// ServiceName returns the Restate Virtual Object service name for the
// IAMGroup driver. The orchestrator uses this to dispatch durable RPCs.
func (a *IAMGroupAdapter) ServiceName() string {
	return iamgroup.ServiceName
}

// Scope returns the key-scope strategy for IAMGroup resources,
// which controls how BuildKey assembles the canonical object key.
func (a *IAMGroupAdapter) Scope() KeyScope {
	return KeyScopeGlobal
}

// BuildKey derives the canonical Restate object key for a IAMGroup resource
// from the raw JSON resource document. The key is composed of group name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *IAMGroupAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("group name", spec.GroupName); err != nil {
		return "", err
	}
	return spec.GroupName, nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete IAMGroup spec struct expected by the driver.
func (a *IAMGroupAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the IAMGroup Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *IAMGroupAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[iamgroup.IAMGroupSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[iamgroup.IAMGroupSpec, iamgroup.IAMGroupOutputs](
		restate.Object[iamgroup.IAMGroupOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[iamgroup.IAMGroupOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the IAMGroup Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *IAMGroupAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed IAMGroup driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *IAMGroupAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[iamgroup.IAMGroupOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"arn": out.Arn, "groupId": out.GroupId, "groupName": out.GroupName}, nil
}

// Plan compares the desired IAMGroup spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *IAMGroupAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[iamgroup.IAMGroupSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}

	type describePlanResult struct {
		State iamgroup.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeGroup(runCtx, desired.GroupName)
		if descErr != nil {
			if iamgroup.IsNotFound(descErr) {
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

	rawDiffs := iamgroup.ComputeFieldDiffs(desired, result.State)
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
// an existing IAMGroup resource by its region and provider-native ID.
func (a *IAMGroupAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return resourceID, nil
}

// Import adopts an existing IAMGroup resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *IAMGroupAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, iamgroup.IAMGroupOutputs](
		restate.Object[iamgroup.IAMGroupOutputs](ctx, a.ServiceName(), key, "Import"),
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
// the typed IAMGroup spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *IAMGroupAdapter) decodeSpec(doc resourceDocument) (iamgroup.IAMGroupSpec, error) {
	var spec struct {
		Path              string            `json:"path"`
		InlinePolicies    map[string]string `json:"inlinePolicies"`
		ManagedPolicyArns []string          `json:"managedPolicyArns"`
	}
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return iamgroup.IAMGroupSpec{}, fmt.Errorf("decode IAMGroup spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return iamgroup.IAMGroupSpec{}, fmt.Errorf("IAMGroup metadata.name is required")
	}
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.InlinePolicies == nil {
		spec.InlinePolicies = map[string]string{}
	}
	if spec.ManagedPolicyArns == nil {
		spec.ManagedPolicyArns = []string{}
	}
	return iamgroup.IAMGroupSpec{Path: spec.Path, GroupName: name, InlinePolicies: spec.InlinePolicies, ManagedPolicyArns: spec.ManagedPolicyArns}, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *IAMGroupAdapter) planningAPI(ctx restate.Context, account string) (iamgroup.IAMGroupAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("IAMGroup adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAMGroup planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
