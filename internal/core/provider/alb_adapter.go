// ALB provider adapter.
//
// This file implements the provider.Adapter interface for Amazon ELBv2 (Application Load Balancer)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the ALB Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + ALB name.
// Application load balancers are region-scoped; the key combines the AWS region and ALB name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/alb"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// ALBAdapter implements provider.Adapter for ALB (Amazon ELBv2 (Application Load Balancer)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type ALBAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI alb.ALBAPI
	apiFactory        func(aws.Config) alb.ALBAPI
}

// NewALBAdapterWithAuth creates a production ALB adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewALBAdapterWithAuth(auth authservice.AuthClient) *ALBAdapter {
	return &ALBAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) alb.ALBAPI {
			return alb.NewALBAPI(awsclient.NewELBv2Client(cfg))
		},
	}
}

// NewALBAdapterWithAPI creates a ALB adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewALBAdapterWithAPI(api alb.ALBAPI) *ALBAdapter {
	return &ALBAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "ALB" that maps template
// resource documents to this adapter in the provider registry.
func (a *ALBAdapter) Kind() string { return alb.ServiceName }

// ServiceName returns the Restate Virtual Object service name for the
// ALB driver. The orchestrator uses this to dispatch durable RPCs.
func (a *ALBAdapter) ServiceName() string { return alb.ServiceName }

// Scope returns the key-scope strategy for ALB resources,
// which controls how BuildKey assembles the canonical object key.
func (a *ALBAdapter) Scope() KeyScope { return KeyScopeRegion }

// BuildKey derives the canonical Restate object key for a ALB resource
// from the raw JSON resource document. The key is composed of region + ALB name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *ALBAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("ALB name", spec.Name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.Name), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete ALB spec struct expected by the driver.
func (a *ALBAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the ALB Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *ALBAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[alb.ALBSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[alb.ALBSpec, alb.ALBOutputs](restate.Object[alb.ALBOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[alb.ALBOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the ALB Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *ALBAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed ALB driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *ALBAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[alb.ALBOutputs](raw)
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

// Plan compares the desired ALB spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *ALBAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[alb.ALBSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[alb.ALBOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("ALB Plan: failed to read outputs for key %q: %w", key, getErr)
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
		State alb.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeALB(runCtx, identifier)
		if descErr != nil {
			if alb.IsNotFound(descErr) {
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
	rawDiffs := alb.ComputeFieldDiffs(desired, result.State)
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
// an existing ALB resource by its region and provider-native ID.
func (a *ALBAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing ALB resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *ALBAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, alb.ALBOutputs](restate.Object[alb.ALBOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
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
// the typed ALB spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *ALBAdapter) decodeSpec(doc resourceDocument) (alb.ALBSpec, error) {
	var spec alb.ALBSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return alb.ALBSpec{}, fmt.Errorf("decode ALB spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return alb.ALBSpec{}, fmt.Errorf("ALB metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return alb.ALBSpec{}, fmt.Errorf("ALB spec.region is required")
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
func (a *ALBAdapter) planningAPI(ctx restate.Context, account string) (alb.ALBAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("ALB adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve ALB planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
