// Listener provider adapter.
//
// This file implements the provider.Adapter interface for Amazon ELBv2 (Listener)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the Listener Restate Virtual Object driver.
//
// Key scope: custom.
// Key parts: load balancer ARN + port.
// Listeners are scoped to a load balancer; the key combines the LB ARN and port.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/listener"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// ListenerAdapter implements provider.Adapter for Listener (Amazon ELBv2 (Listener)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type ListenerAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI listener.ListenerAPI
	apiFactory        func(aws.Config) listener.ListenerAPI
}

// NewListenerAdapterWithAuth creates a production Listener adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewListenerAdapterWithAuth(auth authservice.AuthClient) *ListenerAdapter {
	return &ListenerAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) listener.ListenerAPI {
			return listener.NewListenerAPI(awsclient.NewELBv2Client(cfg))
		},
	}
}

// NewListenerAdapterWithAPI creates a Listener adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewListenerAdapterWithAPI(api listener.ListenerAPI) *ListenerAdapter {
	return &ListenerAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "Listener" that maps template
// resource documents to this adapter in the provider registry.
func (a *ListenerAdapter) Kind() string { return listener.ServiceName }

// ServiceName returns the Restate Virtual Object service name for the
// Listener driver. The orchestrator uses this to dispatch durable RPCs.
func (a *ListenerAdapter) ServiceName() string { return listener.ServiceName }

// Scope returns the key-scope strategy for Listener resources,
// which controls how BuildKey assembles the canonical object key.
func (a *ListenerAdapter) Scope() KeyScope { return KeyScopeRegion }

// BuildKey derives the canonical Restate object key for a Listener resource
// from the raw JSON resource document. The key is composed of load balancer ARN + port,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *ListenerAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	region := extractRegionFromLBArn(spec.LoadBalancerArn)
	if region == "" {
		return "", fmt.Errorf("cannot extract region from loadBalancerArn %q", spec.LoadBalancerArn)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if err := ValidateKeyPart("listener name", name); err != nil {
		return "", err
	}
	return JoinKey(region, name), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete Listener spec struct expected by the driver.
func (a *ListenerAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the Listener Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *ListenerAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[listener.ListenerSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[listener.ListenerSpec, listener.ListenerOutputs](restate.Object[listener.ListenerOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[listener.ListenerOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the Listener Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *ListenerAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed Listener driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *ListenerAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[listener.ListenerOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"listenerArn": out.ListenerArn,
		"port":        out.Port,
		"protocol":    out.Protocol,
	}, nil
}

// Plan compares the desired Listener spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *ListenerAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[listener.ListenerSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[listener.ListenerOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("listener plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.ListenerArn == "" {
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
		State listener.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeListener(runCtx, outputs.ListenerArn)
		if descErr != nil {
			if listener.IsNotFound(descErr) {
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
	rawDiffs := listener.ComputeFieldDiffs(desired, result.State)
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
// an existing Listener resource by its region and provider-native ID.
func (a *ListenerAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing Listener resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *ListenerAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, listener.ListenerOutputs](restate.Object[listener.ListenerOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
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
// the typed Listener spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *ListenerAdapter) decodeSpec(doc resourceDocument) (listener.ListenerSpec, error) {
	var spec listener.ListenerSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return listener.ListenerSpec{}, fmt.Errorf("decode Listener spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return listener.ListenerSpec{}, fmt.Errorf("listener metadata.name is required")
	}
	if spec.LoadBalancerArn == "" {
		return listener.ListenerSpec{}, fmt.Errorf("listener spec.loadBalancerArn is required")
	}
	spec.Account = ""
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	if spec.Tags["praxis:listener-name"] == "" {
		spec.Tags["praxis:listener-name"] = name
	}
	return spec, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *ListenerAdapter) planningAPI(ctx restate.Context, account string) (listener.ListenerAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("listener adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Listener planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}

func extractRegionFromLBArn(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 4 {
		return parts[3]
	}
	return ""
}
