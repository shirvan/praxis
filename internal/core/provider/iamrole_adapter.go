// IAMRole provider adapter.
//
// This file implements the provider.Adapter interface for AWS IAM (Role)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the IAMRole Restate Virtual Object driver.
//
// Key scope: global (IAM is region-free).
// Key parts: role name (optionally with path prefix).
// IAM roles are global; the key is derived from the role name.
package provider

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/iamrole"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// IAMRoleAdapter implements provider.Adapter for IAMRole (AWS IAM (Role)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type IAMRoleAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI iamrole.IAMRoleAPI
	apiFactory        func(aws.Config) iamrole.IAMRoleAPI
}

// NewIAMRoleAdapterWithAuth creates a production IAMRole adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewIAMRoleAdapterWithAuth(auth authservice.AuthClient) *IAMRoleAdapter {
	return &IAMRoleAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) iamrole.IAMRoleAPI {
			return iamrole.NewIAMRoleAPI(awsclient.NewIAMClient(cfg))
		},
	}
}

// NewIAMRoleAdapterWithAPI creates a IAMRole adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewIAMRoleAdapterWithAPI(api iamrole.IAMRoleAPI) *IAMRoleAdapter {
	return &IAMRoleAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "IAMRole" that maps template
// resource documents to this adapter in the provider registry.
func (a *IAMRoleAdapter) Kind() string {
	return iamrole.ServiceName
}

// ServiceName returns the Restate Virtual Object service name for the
// IAMRole driver. The orchestrator uses this to dispatch durable RPCs.
func (a *IAMRoleAdapter) ServiceName() string {
	return iamrole.ServiceName
}

// Scope returns the key-scope strategy for IAMRole resources,
// which controls how BuildKey assembles the canonical object key.
func (a *IAMRoleAdapter) Scope() KeyScope {
	return KeyScopeGlobal
}

// BuildKey derives the canonical Restate object key for a IAMRole resource
// from the raw JSON resource document. The key is composed of role name (optionally with path prefix),
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *IAMRoleAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("role name", spec.RoleName); err != nil {
		return "", err
	}
	return spec.RoleName, nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete IAMRole spec struct expected by the driver.
func (a *IAMRoleAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the IAMRole Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *IAMRoleAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[iamrole.IAMRoleSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[iamrole.IAMRoleSpec, iamrole.IAMRoleOutputs](
		restate.Object[iamrole.IAMRoleOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[iamrole.IAMRoleOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the IAMRole Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *IAMRoleAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// PreDelete detaches all policies and instance profiles before the role is deleted.
func (a *IAMRoleAdapter) PreDelete(ctx restate.Context, key string) error {
	_, err := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "PreDelete"),
	).Request(restate.Void{})
	return err
}

// NormalizeOutputs converts the typed IAMRole driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *IAMRoleAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[iamrole.IAMRoleOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"arn": out.Arn, "roleId": out.RoleId, "roleName": out.RoleName}, nil
}

// Plan compares the desired IAMRole spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *IAMRoleAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[iamrole.IAMRoleSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}

	type describePlanResult struct {
		State iamrole.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeRole(runCtx, desired.RoleName)
		if descErr != nil {
			if iamrole.IsNotFound(descErr) {
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

	rawDiffs := iamrole.ComputeFieldDiffs(desired, result.State)
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
// an existing IAMRole resource by its region and provider-native ID.
func (a *IAMRoleAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return resourceID, nil
}

// Import adopts an existing IAMRole resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *IAMRoleAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, iamrole.IAMRoleOutputs](
		restate.Object[iamrole.IAMRoleOutputs](ctx, a.ServiceName(), key, "Import"),
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

// Lookup performs a read-only data-source query for an existing IAMRole
// resource, matching by ID, name, or tags. This is used by template data
// source blocks to resolve references to pre-existing infrastructure.
func (a *IAMRoleAdapter) Lookup(ctx restate.Context, account string, filter LookupFilter) (map[string]any, error) {
	api, err := a.planningAPI(ctx, account)
	if err != nil {
		return nil, restate.TerminalError(err, 500)
	}
	observed, err := restate.Run(ctx, func(runCtx restate.RunContext) (iamrole.ObservedState, error) {
		obs, runErr := lookupIAMRole(runCtx, api, filter)
		if runErr != nil {
			return obs, classifyLookupError(runErr, iamrole.IsNotFound)
		}
		return obs, nil
	})
	if err != nil {
		return nil, err
	}
	if !matchesIAMRoleFilter(observed, filter) {
		return nil, restate.TerminalError(fmt.Errorf("data source lookup: no IAMRole found matching filter"), 404)
	}
	return a.NormalizeOutputs(iamrole.IAMRoleOutputs{Arn: observed.Arn, RoleId: observed.RoleId, RoleName: observed.RoleName})
}

// decodeSpec unmarshals the raw JSON spec from a resource document into
// the typed IAMRole spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *IAMRoleAdapter) decodeSpec(doc resourceDocument) (iamrole.IAMRoleSpec, error) {
	var spec struct {
		Path                     string            `json:"path"`
		AssumeRolePolicyDocument string            `json:"assumeRolePolicyDocument"`
		Description              string            `json:"description"`
		MaxSessionDuration       int32             `json:"maxSessionDuration"`
		PermissionsBoundary      string            `json:"permissionsBoundary"`
		InlinePolicies           map[string]string `json:"inlinePolicies"`
		ManagedPolicyArns        []string          `json:"managedPolicyArns"`
		Tags                     map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return iamrole.IAMRoleSpec{}, fmt.Errorf("decode IAMRole spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return iamrole.IAMRoleSpec{}, fmt.Errorf("IAMRole metadata.name is required")
	}
	if strings.TrimSpace(spec.AssumeRolePolicyDocument) == "" {
		return iamrole.IAMRoleSpec{}, fmt.Errorf("IAMRole spec.assumeRolePolicyDocument is required")
	}
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.MaxSessionDuration == 0 {
		spec.MaxSessionDuration = 3600
	}
	if spec.InlinePolicies == nil {
		spec.InlinePolicies = map[string]string{}
	}
	if spec.ManagedPolicyArns == nil {
		spec.ManagedPolicyArns = []string{}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return iamrole.IAMRoleSpec{
		Path:                     spec.Path,
		RoleName:                 name,
		AssumeRolePolicyDocument: spec.AssumeRolePolicyDocument,
		Description:              spec.Description,
		MaxSessionDuration:       spec.MaxSessionDuration,
		PermissionsBoundary:      spec.PermissionsBoundary,
		InlinePolicies:           spec.InlinePolicies,
		ManagedPolicyArns:        spec.ManagedPolicyArns,
		Tags:                     spec.Tags,
	}, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *IAMRoleAdapter) planningAPI(ctx restate.Context, account string) (iamrole.IAMRoleAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("IAMRole adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAMRole planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}

func lookupIAMRole(ctx restate.RunContext, api iamrole.IAMRoleAPI, filter LookupFilter) (iamrole.ObservedState, error) {
	roleName := normalizeIAMRoleLookupName(filter)
	if roleName == "" && len(filter.Tag) > 0 {
		resolved, err := api.FindByTags(ctx, filter.Tag)
		if err != nil {
			return iamrole.ObservedState{}, err
		}
		roleName = strings.TrimSpace(resolved)
	}
	if roleName == "" {
		return iamrole.ObservedState{}, fmt.Errorf("not found")
	}
	return api.DescribeRole(ctx, roleName)
}

func normalizeIAMRoleLookupName(filter LookupFilter) string {
	value := strings.TrimSpace(filter.ID)
	if value == "" {
		value = strings.TrimSpace(filter.Name)
	}
	if value == "" {
		return ""
	}
	if strings.Contains(value, ":role/") {
		return path.Base(value)
	}
	return value
}

func matchesIAMRoleFilter(observed iamrole.ObservedState, filter LookupFilter) bool {
	lookupName := normalizeIAMRoleLookupName(filter)
	if lookupName != "" && observed.RoleName != lookupName {
		return false
	}
	for key, value := range filter.Tag {
		if observed.Tags[key] != value {
			return false
		}
	}
	return true
}
