// NATGateway provider adapter.
//
// This file implements the provider.Adapter interface for Amazon VPC (NAT Gateway)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the NATGateway Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + NAT gateway name.
// NAT gateways are region-scoped; the key combines the AWS region and the Name tag.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/natgw"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// NATGatewayAdapter implements provider.Adapter for NATGateway (Amazon VPC (NAT Gateway)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type NATGatewayAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI natgw.NATGatewayAPI
	apiFactory        func(aws.Config) natgw.NATGatewayAPI
}

// NewNATGatewayAdapterWithAuth creates a production NATGateway adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewNATGatewayAdapterWithAuth(auth authservice.AuthClient) *NATGatewayAdapter {
	return &NATGatewayAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) natgw.NATGatewayAPI {
			return natgw.NewNATGatewayAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

// NewNATGatewayAdapterWithAPI creates a NATGateway adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewNATGatewayAdapterWithAPI(api natgw.NATGatewayAPI) *NATGatewayAdapter {
	return &NATGatewayAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "NATGateway" that maps template
// resource documents to this adapter in the provider registry.
func (a *NATGatewayAdapter) Kind() string {
	return natgw.ServiceName
}

// ServiceName returns the Restate Virtual Object service name for the
// NATGateway driver. The orchestrator uses this to dispatch durable RPCs.
func (a *NATGatewayAdapter) ServiceName() string {
	return natgw.ServiceName
}

// Scope returns the key-scope strategy for NATGateway resources,
// which controls how BuildKey assembles the canonical object key.
func (a *NATGatewayAdapter) Scope() KeyScope {
	return KeyScopeRegion
}

// BuildKey derives the canonical Restate object key for a NATGateway resource
// from the raw JSON resource document. The key is composed of region + NAT gateway name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *NATGatewayAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	name := strings.TrimSpace(doc.Metadata.Name)
	if err := ValidateKeyPart("NAT gateway name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete NATGateway spec struct expected by the driver.
func (a *NATGatewayAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the NATGateway Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *NATGatewayAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[natgw.NATGatewaySpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[natgw.NATGatewaySpec, natgw.NATGatewayOutputs](
		restate.Object[natgw.NATGatewayOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[natgw.NATGatewayOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

// Delete sends a durable Delete request to the NATGateway Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *NATGatewayAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed NATGateway driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *NATGatewayAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[natgw.NATGatewayOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"natGatewayId":       out.NatGatewayId,
		"subnetId":           out.SubnetId,
		"vpcId":              out.VpcId,
		"connectivityType":   out.ConnectivityType,
		"state":              out.State,
		"privateIp":          out.PrivateIp,
		"networkInterfaceId": out.NetworkInterfaceId,
	}
	if out.PublicIp != "" {
		result["publicIp"] = out.PublicIp
	}
	if out.AllocationId != "" {
		result["allocationId"] = out.AllocationId
	}
	return result, nil
}

// Plan compares the desired NATGateway spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *NATGatewayAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[natgw.NATGatewaySpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[natgw.NATGatewayOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("NATGateway Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.NatGatewayId == "" {
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
		State natgw.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeNATGateway(runCtx, outputs.NatGatewayId)
		if descErr != nil {
			if natgw.IsNotFound(descErr) {
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

	rawDiffs := natgw.ComputeFieldDiffs(desired, result.State)
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
// an existing NATGateway resource by its region and provider-native ID.
func (a *NATGatewayAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing NATGateway resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *NATGatewayAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, natgw.NATGatewayOutputs](
		restate.Object[natgw.NATGatewayOutputs](ctx, a.ServiceName(), key, "Import"),
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

// DefaultTimeouts provides per-kind default timeouts for NAT Gateways.
func (a *NATGatewayAdapter) DefaultTimeouts() types.ResourceTimeouts {
	return types.ResourceTimeouts{Create: "10m", Update: "10m", Delete: "10m"}
}

// decodeSpec unmarshals the raw JSON spec from a resource document into
// the typed NATGateway spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *NATGatewayAdapter) decodeSpec(doc resourceDocument) (natgw.NATGatewaySpec, error) {
	var spec natgw.NATGatewaySpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return natgw.NATGatewaySpec{}, fmt.Errorf("decode NATGateway spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return natgw.NATGatewaySpec{}, fmt.Errorf("NATGateway metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return natgw.NATGatewaySpec{}, fmt.Errorf("NATGateway spec.region is required")
	}
	if strings.TrimSpace(spec.SubnetId) == "" {
		return natgw.NATGatewaySpec{}, fmt.Errorf("NATGateway spec.subnetId is required")
	}
	spec = natgw.NATGatewaySpec{
		Account:          "",
		Region:           spec.Region,
		SubnetId:         spec.SubnetId,
		ConnectivityType: spec.ConnectivityType,
		AllocationId:     spec.AllocationId,
		Tags:             spec.Tags,
	}
	spec = natgwSpecWithDefaults(spec)
	if spec.Tags["Name"] == "" {
		spec.Tags["Name"] = name
	}
	if spec.ConnectivityType == "private" && spec.AllocationId != "" {
		return natgw.NATGatewaySpec{}, fmt.Errorf("NATGateway spec.allocationId must be empty for private NAT gateways")
	}
	if spec.ConnectivityType == "public" && strings.TrimSpace(spec.AllocationId) == "" {
		return natgw.NATGatewaySpec{}, fmt.Errorf("NATGateway spec.allocationId is required for public NAT gateways")
	}
	return spec, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *NATGatewayAdapter) planningAPI(ctx restate.Context, account string) (natgw.NATGatewayAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("NATGateway adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve NATGateway planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}

func natgwSpecWithDefaults(spec natgw.NATGatewaySpec) natgw.NATGatewaySpec {
	if spec.ConnectivityType == "" {
		spec.ConnectivityType = "public"
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	return spec
}
