// NLB provider adapter.
//
// This file implements the provider.Adapter interface for Amazon ELBv2 (Network Load Balancer)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the NLB Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + NLB name.
// Network load balancers are region-scoped; the key combines the AWS region and NLB name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/nlb"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// NLBAdapter implements provider.Adapter for NLB (Amazon ELBv2 (Network Load Balancer)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type NLBAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI nlb.NLBAPI
	apiFactory        func(aws.Config) nlb.NLBAPI
}

// NewNLBAdapterWithAuth creates a production NLB adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewNLBAdapterWithAuth(auth authservice.AuthClient) *NLBAdapter {
	return &NLBAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) nlb.NLBAPI {
			return nlb.NewNLBAPI(awsclient.NewELBv2Client(cfg))
		},
	}
}

// NewNLBAdapterWithAPI creates a NLB adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewNLBAdapterWithAPI(api nlb.NLBAPI) *NLBAdapter {
	return &NLBAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "NLB" that maps template
// resource documents to this adapter in the provider registry.
func (a *NLBAdapter) Kind() string { return nlb.ServiceName }

// ServiceName returns the Restate Virtual Object service name for the
// NLB driver. The orchestrator uses this to dispatch durable RPCs.
func (a *NLBAdapter) ServiceName() string { return nlb.ServiceName }

// Scope returns the key-scope strategy for NLB resources,
// which controls how BuildKey assembles the canonical object key.
func (a *NLBAdapter) Scope() KeyScope { return KeyScopeRegion }

// BuildKey derives the canonical Restate object key for a NLB resource
// from the raw JSON resource document. The key is composed of region + NLB name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *NLBAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("NLB name", spec.Name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.Name), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete NLB spec struct expected by the driver.
func (a *NLBAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the NLB Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *NLBAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[nlb.NLBSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[nlb.NLBSpec, nlb.NLBOutputs](restate.Object[nlb.NLBOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[nlb.NLBOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the NLB Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *NLBAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed NLB driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *NLBAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[nlb.NLBOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"loadBalancerArn":       out.LoadBalancerArn,
		"dnsName":               out.DnsName,
		"hostedZoneId":          out.HostedZoneId,
		"vpcId":                 out.VpcId,
		"canonicalHostedZoneId": out.CanonicalHostedZoneId,
	}, nil
}

// Plan compares the desired NLB spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *NLBAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[nlb.NLBSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[nlb.NLBOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("NLB Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}
	identifier := outputs.LoadBalancerArn
	if identifier == "" {
		identifier = desired.Name
	}
	type describePlanResult struct {
		State nlb.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeNLB(runCtx, identifier)
		if descErr != nil {
			if nlb.IsNotFound(descErr) {
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
	rawDiffs := nlb.ComputeFieldDiffs(desired, result.State)
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
// an existing NLB resource by its region and provider-native ID.
func (a *NLBAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing NLB resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *NLBAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, nlb.NLBOutputs](restate.Object[nlb.NLBOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

// DefaultTimeouts provides per-kind default timeouts for NLB resources.
func (a *NLBAdapter) DefaultTimeouts() types.ResourceTimeouts {
	return types.ResourceTimeouts{Create: "10m", Update: "10m", Delete: "10m"}
}

// decodeSpec unmarshals the raw JSON spec from a resource document into
// the typed NLB spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *NLBAdapter) decodeSpec(doc resourceDocument) (nlb.NLBSpec, error) {
	var spec nlb.NLBSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return nlb.NLBSpec{}, fmt.Errorf("decode NLB spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return nlb.NLBSpec{}, fmt.Errorf("NLB metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return nlb.NLBSpec{}, fmt.Errorf("NLB spec.region is required")
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
func (a *NLBAdapter) planningAPI(ctx restate.Context, account string) (nlb.NLBAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("NLB adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve NLB planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
