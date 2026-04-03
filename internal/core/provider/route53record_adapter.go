// Route53Record provider adapter.
//
// This file implements the provider.Adapter interface for Amazon Route 53 (Record)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the Route53Record Restate Virtual Object driver.
//
// Key scope: custom.
// Key parts: zone ID + record name + record type.
// Route 53 records are scoped to a hosted zone; the key combines zone ID, record name, and record type.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/route53record"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// Route53RecordAdapter implements provider.Adapter for Route53Record (Amazon Route 53 (Record)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type Route53RecordAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI route53record.RecordAPI
	apiFactory        func(aws.Config) route53record.RecordAPI
}

// NewRoute53RecordAdapterWithAuth creates a production Route53Record adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewRoute53RecordAdapterWithAuth(auth authservice.AuthClient) *Route53RecordAdapter {
	return &Route53RecordAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) route53record.RecordAPI {
			return route53record.NewRecordAPI(awsclient.NewRoute53Client(cfg))
		},
	}
}

// NewRoute53RecordAdapterWithAPI creates a Route53Record adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewRoute53RecordAdapterWithAPI(api route53record.RecordAPI) *Route53RecordAdapter {
	return &Route53RecordAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "Route53Record" that maps template
// resource documents to this adapter in the provider registry.
func (a *Route53RecordAdapter) Kind() string        { return route53record.ServiceName }
// ServiceName returns the Restate Virtual Object service name for the
// Route53Record driver. The orchestrator uses this to dispatch durable RPCs.
func (a *Route53RecordAdapter) ServiceName() string { return route53record.ServiceName }
// Scope returns the key-scope strategy for Route53Record resources,
// which controls how BuildKey assembles the canonical object key.
func (a *Route53RecordAdapter) Scope() KeyScope     { return KeyScopeCustom }

// BuildKey derives the canonical Restate object key for a Route53Record resource
// from the raw JSON resource document. The key is composed of zone ID + record name + record type,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *Route53RecordAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("hosted zone ID", spec.HostedZoneId); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("record name", spec.Name); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("record type", spec.Type); err != nil {
		return "", err
	}
	parts := []string{spec.HostedZoneId, spec.Name, spec.Type}
	if spec.SetIdentifier != "" {
		if err := ValidateKeyPart("record set identifier", spec.SetIdentifier); err != nil {
			return "", err
		}
		parts = append(parts, spec.SetIdentifier)
	}
	return JoinKey(parts...), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete Route53Record spec struct expected by the driver.
func (a *Route53RecordAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the Route53Record Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *Route53RecordAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[route53record.RecordSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[route53record.RecordSpec, route53record.RecordOutputs](restate.Object[route53record.RecordOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[route53record.RecordOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the Route53Record Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *Route53RecordAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed Route53Record driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *Route53RecordAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[route53record.RecordOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"hostedZoneId": out.HostedZoneId, "fqdn": out.FQDN, "type": out.Type}
	if out.SetIdentifier != "" {
		result["setIdentifier"] = out.SetIdentifier
	}
	return result, nil
}

// Plan compares the desired Route53Record spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *Route53RecordAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[route53record.RecordSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[route53record.RecordOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("Route53Record Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.FQDN == "" {
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
		State route53record.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeRecord(runCtx, route53record.RecordIdentity{HostedZoneId: outputs.HostedZoneId, Name: outputs.FQDN, Type: outputs.Type, SetIdentifier: outputs.SetIdentifier})
		if descErr != nil {
			if route53record.IsNotFound(descErr) {
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
	rawDiffs := route53record.ComputeFieldDiffs(desired, result.State)
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
// an existing Route53Record resource by its region and provider-native ID.
func (a *Route53RecordAdapter) BuildImportKey(region, resourceID string) (string, error) {
	trimmed := strings.TrimSpace(resourceID)
	if trimmed == "" {
		return "", fmt.Errorf("resource ID is required to build a resource key")
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) == 1 {
		parts = strings.Split(trimmed, "~")
	}
	if len(parts) < 3 || len(parts) > 4 {
		return "", fmt.Errorf("Route53Record import resource ID must be <hostedZoneId>/<fqdn>/<type>[/<setIdentifier>]")
	}
	return JoinKey(parts...), nil
}

// Import adopts an existing Route53Record resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *Route53RecordAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, route53record.RecordOutputs](restate.Object[route53record.RecordOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
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
// the typed Route53Record spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *Route53RecordAdapter) decodeSpec(doc resourceDocument) (route53record.RecordSpec, error) {
	var spec route53record.RecordSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return route53record.RecordSpec{}, fmt.Errorf("decode Route53Record spec: %w", err)
	}
	spec.HostedZoneId = strings.TrimSpace(strings.TrimPrefix(spec.HostedZoneId, "/hostedzone/"))
	spec.Name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(spec.Name), "."))
	spec.Type = strings.ToUpper(strings.TrimSpace(spec.Type))
	spec.SetIdentifier = strings.TrimSpace(spec.SetIdentifier)
	spec.Account = ""
	return spec, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *Route53RecordAdapter) planningAPI(ctx restate.Context, account string) (route53record.RecordAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("Route53Record adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Route53Record planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
