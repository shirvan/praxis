// Route53HealthCheck provider adapter.
//
// This file implements the provider.Adapter interface for Amazon Route 53 (Health Check)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the Route53HealthCheck Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + health check name.
// Route 53 health checks are region-scoped; the key combines region and the Name tag.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/route53healthcheck"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// Route53HealthCheckAdapter implements provider.Adapter for Route53HealthCheck (Amazon Route 53 (Health Check)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type Route53HealthCheckAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI route53healthcheck.HealthCheckAPI
	apiFactory        func(aws.Config) route53healthcheck.HealthCheckAPI
}

// NewRoute53HealthCheckAdapterWithAuth creates a production Route53HealthCheck adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewRoute53HealthCheckAdapterWithAuth(auth authservice.AuthClient) *Route53HealthCheckAdapter {
	return &Route53HealthCheckAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) route53healthcheck.HealthCheckAPI {
			return route53healthcheck.NewHealthCheckAPI(awsclient.NewRoute53Client(cfg))
		},
	}
}

// NewRoute53HealthCheckAdapterWithAPI creates a Route53HealthCheck adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewRoute53HealthCheckAdapterWithAPI(api route53healthcheck.HealthCheckAPI) *Route53HealthCheckAdapter {
	return &Route53HealthCheckAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "Route53HealthCheck" that maps template
// resource documents to this adapter in the provider registry.
func (a *Route53HealthCheckAdapter) Kind() string { return route53healthcheck.ServiceName }

// ServiceName returns the Restate Virtual Object service name for the
// Route53HealthCheck driver. The orchestrator uses this to dispatch durable RPCs.
func (a *Route53HealthCheckAdapter) ServiceName() string { return route53healthcheck.ServiceName }

// Scope returns the key-scope strategy for Route53HealthCheck resources,
// which controls how BuildKey assembles the canonical object key.
func (a *Route53HealthCheckAdapter) Scope() KeyScope { return KeyScopeGlobal }

// BuildKey derives the canonical Restate object key for a Route53HealthCheck resource
// from the raw JSON resource document. The key is composed of region + health check name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *Route53HealthCheckAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("health check name", strings.TrimSpace(doc.Metadata.Name)); err != nil {
		return "", err
	}
	return strings.TrimSpace(doc.Metadata.Name), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete Route53HealthCheck spec struct expected by the driver.
func (a *Route53HealthCheckAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the Route53HealthCheck Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *Route53HealthCheckAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[route53healthcheck.HealthCheckSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[route53healthcheck.HealthCheckSpec, route53healthcheck.HealthCheckOutputs](restate.Object[route53healthcheck.HealthCheckOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[route53healthcheck.HealthCheckOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the Route53HealthCheck Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *Route53HealthCheckAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed Route53HealthCheck driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *Route53HealthCheckAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[route53healthcheck.HealthCheckOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"healthCheckId": out.HealthCheckId}, nil
}

// Plan compares the desired Route53HealthCheck spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *Route53HealthCheckAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[route53healthcheck.HealthCheckSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[route53healthcheck.HealthCheckOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("Route53HealthCheck Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.HealthCheckId == "" {
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
		State route53healthcheck.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeHealthCheck(runCtx, outputs.HealthCheckId)
		if descErr != nil {
			if route53healthcheck.IsNotFound(descErr) {
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
	rawDiffs := route53healthcheck.ComputeFieldDiffs(desired, result.State)
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
// an existing Route53HealthCheck resource by its region and provider-native ID.
func (a *Route53HealthCheckAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return strings.TrimSpace(resourceID), nil
}

// Import adopts an existing Route53HealthCheck resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *Route53HealthCheckAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, route53healthcheck.HealthCheckOutputs](restate.Object[route53healthcheck.HealthCheckOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
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
// the typed Route53HealthCheck spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *Route53HealthCheckAdapter) decodeSpec(doc resourceDocument) (route53healthcheck.HealthCheckSpec, error) {
	var spec route53healthcheck.HealthCheckSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return route53healthcheck.HealthCheckSpec{}, fmt.Errorf("decode Route53HealthCheck spec: %w", err)
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.Account = ""
	return spec, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *Route53HealthCheckAdapter) planningAPI(ctx restate.Context, account string) (route53healthcheck.HealthCheckAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("Route53HealthCheck adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Route53HealthCheck planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
