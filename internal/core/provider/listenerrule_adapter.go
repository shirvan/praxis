// ListenerRule provider adapter.
//
// This file implements the provider.Adapter interface for Amazon ELBv2 (Listener Rule)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the ListenerRule Restate Virtual Object driver.
//
// Key scope: custom.
// Key parts: listener ARN + rule priority.
// Listener rules are scoped to a listener; the key combines the listener ARN and rule priority.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/listenerrule"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// ListenerRuleAdapter implements provider.Adapter for ListenerRule (Amazon ELBv2 (Listener Rule)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type ListenerRuleAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI listenerrule.ListenerRuleAPI
	apiFactory        func(aws.Config) listenerrule.ListenerRuleAPI
}

// NewListenerRuleAdapterWithAuth creates a production ListenerRule adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewListenerRuleAdapterWithAuth(auth authservice.AuthClient) *ListenerRuleAdapter {
	return &ListenerRuleAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) listenerrule.ListenerRuleAPI {
			return listenerrule.NewListenerRuleAPI(awsclient.NewELBv2Client(cfg))
		},
	}
}

// NewListenerRuleAdapterWithAPI creates a ListenerRule adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewListenerRuleAdapterWithAPI(api listenerrule.ListenerRuleAPI) *ListenerRuleAdapter {
	return &ListenerRuleAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "ListenerRule" that maps template
// resource documents to this adapter in the provider registry.
func (a *ListenerRuleAdapter) Kind() string { return listenerrule.ServiceName }

// ServiceName returns the Restate Virtual Object service name for the
// ListenerRule driver. The orchestrator uses this to dispatch durable RPCs.
func (a *ListenerRuleAdapter) ServiceName() string { return listenerrule.ServiceName }

// Scope returns the key-scope strategy for ListenerRule resources,
// which controls how BuildKey assembles the canonical object key.
func (a *ListenerRuleAdapter) Scope() KeyScope { return KeyScopeRegion }

// BuildKey derives the canonical Restate object key for a ListenerRule resource
// from the raw JSON resource document. The key is composed of listener ARN + rule priority,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *ListenerRuleAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	region := extractRegionFromListenerArn(spec.ListenerArn)
	if region == "" {
		return "", fmt.Errorf("cannot extract region from listenerArn %q", spec.ListenerArn)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if err := ValidateKeyPart("listener rule name", name); err != nil {
		return "", err
	}
	return JoinKey(region, name), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete ListenerRule spec struct expected by the driver.
func (a *ListenerRuleAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the ListenerRule Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *ListenerRuleAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[listenerrule.ListenerRuleSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[listenerrule.ListenerRuleSpec, listenerrule.ListenerRuleOutputs](restate.Object[listenerrule.ListenerRuleOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[listenerrule.ListenerRuleOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the ListenerRule Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *ListenerRuleAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed ListenerRule driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *ListenerRuleAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[listenerrule.ListenerRuleOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ruleArn":  out.RuleArn,
		"priority": out.Priority,
	}, nil
}

// Plan compares the desired ListenerRule spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *ListenerRuleAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[listenerrule.ListenerRuleSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[listenerrule.ListenerRuleOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("ListenerRule Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.RuleArn == "" {
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
		State listenerrule.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeRule(runCtx, outputs.RuleArn)
		if descErr != nil {
			if listenerrule.IsNotFound(descErr) {
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
	rawDiffs := listenerrule.ComputeFieldDiffs(desired, result.State)
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
// an existing ListenerRule resource by its region and provider-native ID.
func (a *ListenerRuleAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing ListenerRule resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *ListenerRuleAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, listenerrule.ListenerRuleOutputs](restate.Object[listenerrule.ListenerRuleOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
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
// the typed ListenerRule spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *ListenerRuleAdapter) decodeSpec(doc resourceDocument) (listenerrule.ListenerRuleSpec, error) {
	var spec listenerrule.ListenerRuleSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return listenerrule.ListenerRuleSpec{}, fmt.Errorf("decode ListenerRule spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return listenerrule.ListenerRuleSpec{}, fmt.Errorf("ListenerRule metadata.name is required")
	}
	if spec.ListenerArn == "" {
		return listenerrule.ListenerRuleSpec{}, fmt.Errorf("ListenerRule spec.listenerArn is required")
	}
	spec.Account = ""
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	if spec.Tags["praxis:rule-name"] == "" {
		spec.Tags["praxis:rule-name"] = name
	}
	return spec, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *ListenerRuleAdapter) planningAPI(ctx restate.Context, account string) (listenerrule.ListenerRuleAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("ListenerRule adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve ListenerRule planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}

func extractRegionFromListenerArn(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 4 {
		return parts[3]
	}
	return ""
}
