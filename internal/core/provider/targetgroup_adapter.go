// TargetGroup provider adapter.
//
// This file implements the provider.Adapter interface for Amazon ELBv2 (Target Group)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the TargetGroup Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + target group name.
// Target groups are region-scoped; the key combines the AWS region and target group name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/targetgroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// TargetGroupAdapter implements provider.Adapter for TargetGroup (Amazon ELBv2 (Target Group)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type TargetGroupAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI targetgroup.TargetGroupAPI
	apiFactory        func(aws.Config) targetgroup.TargetGroupAPI
}

// NewTargetGroupAdapterWithAuth creates a production TargetGroup adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewTargetGroupAdapterWithAuth(auth authservice.AuthClient) *TargetGroupAdapter {
	return &TargetGroupAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) targetgroup.TargetGroupAPI {
			return targetgroup.NewTargetGroupAPI(awsclient.NewELBv2Client(cfg))
		},
	}
}

// NewTargetGroupAdapterWithAPI creates a TargetGroup adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewTargetGroupAdapterWithAPI(api targetgroup.TargetGroupAPI) *TargetGroupAdapter {
	return &TargetGroupAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "TargetGroup" that maps template
// resource documents to this adapter in the provider registry.
func (a *TargetGroupAdapter) Kind() string { return targetgroup.ServiceName }

// ServiceName returns the Restate Virtual Object service name for the
// TargetGroup driver. The orchestrator uses this to dispatch durable RPCs.
func (a *TargetGroupAdapter) ServiceName() string { return targetgroup.ServiceName }

// Scope returns the key-scope strategy for TargetGroup resources,
// which controls how BuildKey assembles the canonical object key.
func (a *TargetGroupAdapter) Scope() KeyScope { return KeyScopeRegion }

// BuildKey derives the canonical Restate object key for a TargetGroup resource
// from the raw JSON resource document. The key is composed of region + target group name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *TargetGroupAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("target group name", spec.Name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.Name), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete TargetGroup spec struct expected by the driver.
func (a *TargetGroupAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the TargetGroup Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *TargetGroupAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[targetgroup.TargetGroupSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs](restate.Object[targetgroup.TargetGroupOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[targetgroup.TargetGroupOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the TargetGroup Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *TargetGroupAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed TargetGroup driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *TargetGroupAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[targetgroup.TargetGroupOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"targetGroupArn": out.TargetGroupArn, "targetGroupName": out.TargetGroupName}, nil
}

// Plan compares the desired TargetGroup spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *TargetGroupAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[targetgroup.TargetGroupSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[targetgroup.TargetGroupOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("TargetGroup Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}
	identifier := outputs.TargetGroupArn
	if identifier == "" {
		identifier = desired.Name
	}
	type describePlanResult struct {
		State targetgroup.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeTargetGroup(runCtx, identifier)
		if descErr != nil {
			if targetgroup.IsNotFound(descErr) {
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
	rawDiffs := targetgroup.ComputeFieldDiffs(desired, result.State)
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
// an existing TargetGroup resource by its region and provider-native ID.
func (a *TargetGroupAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing TargetGroup resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *TargetGroupAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, targetgroup.TargetGroupOutputs](restate.Object[targetgroup.TargetGroupOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
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
// the typed TargetGroup spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *TargetGroupAdapter) decodeSpec(doc resourceDocument) (targetgroup.TargetGroupSpec, error) {
	var spec targetgroup.TargetGroupSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return targetgroup.TargetGroupSpec{}, fmt.Errorf("decode TargetGroup spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return targetgroup.TargetGroupSpec{}, fmt.Errorf("TargetGroup metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return targetgroup.TargetGroupSpec{}, fmt.Errorf("TargetGroup spec.region is required")
	}
	spec.Name = name
	spec.Account = ""
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	if spec.Tags["Name"] == "" {
		spec.Tags["Name"] = name
	}
	return spec, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *TargetGroupAdapter) planningAPI(ctx restate.Context, account string) (targetgroup.TargetGroupAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("TargetGroup adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve TargetGroup planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
