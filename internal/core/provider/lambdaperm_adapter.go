// LambdaPermission provider adapter.
//
// This file implements the provider.Adapter interface for AWS Lambda (Permission)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the LambdaPermission Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + function name + statement ID.
// Lambda permissions are region-scoped and attached to a function; the key combines region, function name, and statement ID.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/lambdaperm"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// LambdaPermissionAdapter implements provider.Adapter for LambdaPermission (AWS Lambda (Permission)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type LambdaPermissionAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI lambdaperm.PermissionAPI
	apiFactory        func(aws.Config) lambdaperm.PermissionAPI
}

// NewLambdaPermissionAdapterWithAuth creates a production LambdaPermission adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewLambdaPermissionAdapterWithAuth(auth authservice.AuthClient) *LambdaPermissionAdapter {
	return &LambdaPermissionAdapter{auth: auth, apiFactory: func(cfg aws.Config) lambdaperm.PermissionAPI {
		return lambdaperm.NewPermissionAPI(awsclient.NewLambdaClient(cfg))
	}}
}

// Kind returns the resource kind string "LambdaPermission" that maps template
// resource documents to this adapter in the provider registry.
func (a *LambdaPermissionAdapter) Kind() string { return lambdaperm.ServiceName }

// ServiceName returns the Restate Virtual Object service name for the
// LambdaPermission driver. The orchestrator uses this to dispatch durable RPCs.
func (a *LambdaPermissionAdapter) ServiceName() string { return lambdaperm.ServiceName }

// Scope returns the key-scope strategy for LambdaPermission resources,
// which controls how BuildKey assembles the canonical object key.
func (a *LambdaPermissionAdapter) Scope() KeyScope { return KeyScopeRegion }

// BuildKey derives the canonical Restate object key for a LambdaPermission resource
// from the raw JSON resource document. The key is composed of region + function name + statement ID,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *LambdaPermissionAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("region", spec.Region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("function name", spec.FunctionName); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("statement ID", spec.StatementId); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.FunctionName, spec.StatementId), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete LambdaPermission spec struct expected by the driver.
func (a *LambdaPermissionAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the LambdaPermission Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *LambdaPermissionAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[lambdaperm.LambdaPermissionSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[lambdaperm.LambdaPermissionSpec, lambdaperm.LambdaPermissionOutputs](restate.Object[lambdaperm.LambdaPermissionOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[lambdaperm.LambdaPermissionOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the LambdaPermission Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *LambdaPermissionAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed LambdaPermission driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *LambdaPermissionAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[lambdaperm.LambdaPermissionOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"statementId": out.StatementId, "functionName": out.FunctionName, "statement": out.Statement}, nil
}

// Plan compares the desired LambdaPermission spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *LambdaPermissionAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[lambdaperm.LambdaPermissionSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[lambdaperm.LambdaPermissionOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("LambdaPermission Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.StatementId == "" {
		fields, fieldErr := createFieldDiffsFromSpec(desired)
		if fieldErr != nil {
			return "", nil, fieldErr
		}
		return types.OpCreate, fields, nil
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}
	type describePlanResult struct {
		State lambdaperm.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.GetPermission(runCtx, outputs.FunctionName, outputs.StatementId)
		if descErr != nil {
			if lambdaperm.IsNotFound(descErr) {
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
	rawDiffs := lambdaperm.ComputeFieldDiffs(desired, result.State)
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
// an existing LambdaPermission resource by its region and provider-native ID.
func (a *LambdaPermissionAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	functionName, statementID, err := lambdapermSplitResourceID(resourceID)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("function name", functionName); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("statement ID", statementID); err != nil {
		return "", err
	}
	return JoinKey(region, functionName, statementID), nil
}

// Import adopts an existing LambdaPermission resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *LambdaPermissionAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, lambdaperm.LambdaPermissionOutputs](restate.Object[lambdaperm.LambdaPermissionOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
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
// the typed LambdaPermission spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *LambdaPermissionAdapter) decodeSpec(doc resourceDocument) (lambdaperm.LambdaPermissionSpec, error) {
	var spec lambdaperm.LambdaPermissionSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return lambdaperm.LambdaPermissionSpec{}, fmt.Errorf("decode LambdaPermission spec: %w", err)
	}
	if strings.TrimSpace(spec.Region) == "" {
		return lambdaperm.LambdaPermissionSpec{}, fmt.Errorf("LambdaPermission spec.region is required")
	}
	if strings.TrimSpace(spec.StatementId) == "" {
		spec.StatementId = strings.TrimSpace(doc.Metadata.Name)
	}
	if strings.TrimSpace(spec.StatementId) == "" {
		return lambdaperm.LambdaPermissionSpec{}, fmt.Errorf("LambdaPermission metadata.name or spec.statementId is required")
	}
	return lambdaperm.LambdaPermissionSpec{Region: spec.Region, FunctionName: spec.FunctionName, StatementId: spec.StatementId, Action: spec.Action, Principal: spec.Principal, SourceArn: spec.SourceArn, SourceAccount: spec.SourceAccount, EventSourceToken: spec.EventSourceToken, Qualifier: spec.Qualifier}, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *LambdaPermissionAdapter) planningAPI(ctx restate.Context, account string) (lambdaperm.PermissionAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("lambda permission adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Lambda permission planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}

func lambdapermSplitResourceID(resourceID string) (string, string, error) {
	parts := strings.SplitN(resourceID, "~", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("import resource ID must be functionName~statementId")
	}
	return parts[0], parts[1], nil
}
