// IAMUser provider adapter.
//
// This file implements the provider.Adapter interface for AWS IAM (User)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the IAMUser Restate Virtual Object driver.
//
// Key scope: global (IAM is region-free).
// Key parts: user name.
// IAM users are global; the key is the user name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/iamuser"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// IAMUserAdapter implements provider.Adapter for IAMUser (AWS IAM (User)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type IAMUserAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI iamuser.IAMUserAPI
	apiFactory        func(aws.Config) iamuser.IAMUserAPI
}

// NewIAMUserAdapterWithAuth creates a production IAMUser adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewIAMUserAdapterWithAuth(auth authservice.AuthClient) *IAMUserAdapter {
	return &IAMUserAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) iamuser.IAMUserAPI {
			return iamuser.NewIAMUserAPI(awsclient.NewIAMClient(cfg))
		},
	}
}

// NewIAMUserAdapterWithAPI creates a IAMUser adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewIAMUserAdapterWithAPI(api iamuser.IAMUserAPI) *IAMUserAdapter {
	return &IAMUserAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "IAMUser" that maps template
// resource documents to this adapter in the provider registry.
func (a *IAMUserAdapter) Kind() string {
	return iamuser.ServiceName
}

// ServiceName returns the Restate Virtual Object service name for the
// IAMUser driver. The orchestrator uses this to dispatch durable RPCs.
func (a *IAMUserAdapter) ServiceName() string {
	return iamuser.ServiceName
}

// Scope returns the key-scope strategy for IAMUser resources,
// which controls how BuildKey assembles the canonical object key.
func (a *IAMUserAdapter) Scope() KeyScope {
	return KeyScopeGlobal
}

// BuildKey derives the canonical Restate object key for a IAMUser resource
// from the raw JSON resource document. The key is composed of user name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *IAMUserAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("user name", spec.UserName); err != nil {
		return "", err
	}
	return spec.UserName, nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete IAMUser spec struct expected by the driver.
func (a *IAMUserAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the IAMUser Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *IAMUserAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[iamuser.IAMUserSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[iamuser.IAMUserSpec, iamuser.IAMUserOutputs](
		restate.Object[iamuser.IAMUserOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[iamuser.IAMUserOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the IAMUser Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *IAMUserAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed IAMUser driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *IAMUserAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[iamuser.IAMUserOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"arn": out.Arn, "userId": out.UserId, "userName": out.UserName}, nil
}

// Plan compares the desired IAMUser spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *IAMUserAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[iamuser.IAMUserSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}

	type describePlanResult struct {
		State iamuser.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeUser(runCtx, desired.UserName)
		if descErr != nil {
			if iamuser.IsNotFound(descErr) {
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

	rawDiffs := iamuser.ComputeFieldDiffs(desired, result.State)
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
// an existing IAMUser resource by its region and provider-native ID.
func (a *IAMUserAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return resourceID, nil
}

// Import adopts an existing IAMUser resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *IAMUserAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, iamuser.IAMUserOutputs](
		restate.Object[iamuser.IAMUserOutputs](ctx, a.ServiceName(), key, "Import"),
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
// the typed IAMUser spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *IAMUserAdapter) decodeSpec(doc resourceDocument) (iamuser.IAMUserSpec, error) {
	var spec struct {
		Path                string            `json:"path"`
		PermissionsBoundary string            `json:"permissionsBoundary"`
		InlinePolicies      map[string]string `json:"inlinePolicies"`
		ManagedPolicyArns   []string          `json:"managedPolicyArns"`
		Groups              []string          `json:"groups"`
		Tags                map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return iamuser.IAMUserSpec{}, fmt.Errorf("decode IAMUser spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return iamuser.IAMUserSpec{}, fmt.Errorf("IAMUser metadata.name is required")
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
	if spec.Groups == nil {
		spec.Groups = []string{}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return iamuser.IAMUserSpec{
		Path:                spec.Path,
		UserName:            name,
		PermissionsBoundary: spec.PermissionsBoundary,
		InlinePolicies:      spec.InlinePolicies,
		ManagedPolicyArns:   spec.ManagedPolicyArns,
		Groups:              spec.Groups,
		Tags:                spec.Tags,
	}, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *IAMUserAdapter) planningAPI(ctx restate.Context, account string) (iamuser.IAMUserAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("IAMUser adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAMUser planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
