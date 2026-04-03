// EventSourceMapping provider adapter.
//
// This file implements the provider.Adapter interface for AWS Lambda (Event Source Mapping)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the EventSourceMapping Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + function name + encoded event source key.
// Event source mappings are region-scoped; the key combines region, function name, and an encoded event source identifier.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/esm"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// ESMAdapter implements provider.Adapter for EventSourceMapping (AWS Lambda (Event Source Mapping)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type ESMAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI esm.ESMAPI
	apiFactory        func(aws.Config) esm.ESMAPI
}

// NewESMAdapterWithAuth creates a production EventSourceMapping adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewESMAdapterWithAuth(auth authservice.AuthClient) *ESMAdapter {
	return &ESMAdapter{auth: auth, apiFactory: func(cfg aws.Config) esm.ESMAPI { return esm.NewESMAPI(awsclient.NewLambdaClient(cfg)) }}
}

// Kind returns the resource kind string "EventSourceMapping" that maps template
// resource documents to this adapter in the provider registry.
func (a *ESMAdapter) Kind() string        { return esm.ServiceName }
// ServiceName returns the Restate Virtual Object service name for the
// EventSourceMapping driver. The orchestrator uses this to dispatch durable RPCs.
func (a *ESMAdapter) ServiceName() string { return esm.ServiceName }
// Scope returns the key-scope strategy for EventSourceMapping resources,
// which controls how BuildKey assembles the canonical object key.
func (a *ESMAdapter) Scope() KeyScope     { return KeyScopeRegion }

// BuildKey derives the canonical Restate object key for a EventSourceMapping resource
// from the raw JSON resource document. The key is composed of region + function name + encoded event source key,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *ESMAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	return JoinKey(spec.Region, spec.FunctionName, esm.EncodedEventSourceKey(spec.EventSourceArn)), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete EventSourceMapping spec struct expected by the driver.
func (a *ESMAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the EventSourceMapping Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *ESMAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[esm.EventSourceMappingSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[esm.EventSourceMappingSpec, esm.EventSourceMappingOutputs](restate.Object[esm.EventSourceMappingOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[esm.EventSourceMappingOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the EventSourceMapping Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *ESMAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed EventSourceMapping driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *ESMAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[esm.EventSourceMappingOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"uuid": out.UUID, "eventSourceArn": out.EventSourceArn, "functionArn": out.FunctionArn, "state": out.State, "lastModified": out.LastModified, "batchSize": out.BatchSize}, nil
}

// Plan compares the desired EventSourceMapping spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *ESMAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[esm.EventSourceMappingSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[esm.EventSourceMappingOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("EventSourceMapping Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.UUID == "" {
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
		State esm.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.GetEventSourceMapping(runCtx, outputs.UUID)
		if descErr != nil {
			if esm.IsNotFound(descErr) {
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
	rawDiffs := esm.ComputeFieldDiffs(desired, result.State)
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
// an existing EventSourceMapping resource by its region and provider-native ID.
func (a *ESMAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing EventSourceMapping resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *ESMAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, esm.EventSourceMappingOutputs](restate.Object[esm.EventSourceMappingOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
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
// the typed EventSourceMapping spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *ESMAdapter) decodeSpec(doc resourceDocument) (esm.EventSourceMappingSpec, error) {
	var spec esm.EventSourceMappingSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return esm.EventSourceMappingSpec{}, fmt.Errorf("decode EventSourceMapping spec: %w", err)
	}
	if strings.TrimSpace(spec.Region) == "" {
		return esm.EventSourceMappingSpec{}, fmt.Errorf("EventSourceMapping spec.region is required")
	}
	return spec, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *ESMAdapter) planningAPI(ctx restate.Context, account string) (esm.ESMAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("event source mapping adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve event source mapping planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
