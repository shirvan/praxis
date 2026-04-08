// VPC provider adapter.
//
// This file implements the provider.Adapter interface for Amazon VPC
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the VPC Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + VPC name.
// VPCs are region-scoped, so the key combines the AWS region and the user-provided VPC name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/vpc"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// VPCAdapter implements provider.Adapter for VPC (Amazon VPC) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type VPCAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI vpc.VPCAPI
	apiFactory        func(aws.Config) vpc.VPCAPI
}

// NewVPCAdapterWithAuth creates a production VPC adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewVPCAdapterWithAuth(auth authservice.AuthClient) *VPCAdapter {
	return &VPCAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) vpc.VPCAPI {
			return vpc.NewVPCAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

// NewVPCAdapterWithAPI creates a VPC adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewVPCAdapterWithAPI(api vpc.VPCAPI) *VPCAdapter {
	return &VPCAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "VPC" that maps template
// resource documents to this adapter in the provider registry.
func (a *VPCAdapter) Kind() string {
	return vpc.ServiceName
}

// ServiceName returns the Restate Virtual Object service name for the
// VPC driver. The orchestrator uses this to dispatch durable RPCs.
func (a *VPCAdapter) ServiceName() string {
	return vpc.ServiceName
}

// Scope returns the key-scope strategy for VPC resources,
// which controls how BuildKey assembles the canonical object key.
func (a *VPCAdapter) Scope() KeyScope {
	return KeyScopeRegion
}

// BuildKey derives the canonical Restate object key for a VPC resource
// from the raw JSON resource document. The key is composed of region + VPC name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *VPCAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("VPC name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete VPC spec struct expected by the driver.
func (a *VPCAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the VPC Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *VPCAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[vpc.VPCSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[vpc.VPCSpec, vpc.VPCOutputs](
		restate.Object[vpc.VPCOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[vpc.VPCOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

// Delete sends a durable Delete request to the VPC Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *VPCAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})

	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed VPC driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *VPCAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[vpc.VPCOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"vpcId":              out.VpcId,
		"cidrBlock":          out.CidrBlock,
		"state":              out.State,
		"enableDnsHostnames": out.EnableDnsHostnames,
		"enableDnsSupport":   out.EnableDnsSupport,
		"instanceTenancy":    out.InstanceTenancy,
		"ownerId":            out.OwnerId,
		"dhcpOptionsId":      out.DhcpOptionsId,
		"isDefault":          out.IsDefault,
	}
	if out.ARN != "" {
		result["arn"] = out.ARN
	}
	return result, nil
}

// Plan compares the desired VPC spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *VPCAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[vpc.VPCSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[vpc.VPCOutputs](ctx, a.ServiceName(), key, "GetOutputs").
		Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("VPC Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.VpcId == "" {
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
		State vpc.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeVpc(runCtx, outputs.VpcId)
		if descErr != nil {
			if vpc.IsNotFound(descErr) {
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

	rawDiffs := vpc.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}

	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{
			Path:     diff.Path,
			OldValue: diff.OldValue,
			NewValue: diff.NewValue,
		})
	}
	return types.OpUpdate, fields, nil
}

// BuildImportKey derives the canonical Restate object key for importing
// an existing VPC resource by its region and provider-native ID.
func (a *VPCAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing VPC resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *VPCAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, vpc.VPCOutputs](
		restate.Object[vpc.VPCOutputs](ctx, a.ServiceName(), key, "Import"),
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

// Lookup performs a read-only data-source query for an existing VPC
// resource, matching by ID, name, or tags. This is used by template data
// source blocks to resolve references to pre-existing infrastructure.
func (a *VPCAdapter) Lookup(ctx restate.Context, account string, filter LookupFilter) (map[string]any, error) {
	api, err := a.lookupAPI(ctx, account, filter.Region)
	if err != nil {
		return nil, restate.TerminalError(err, 500)
	}

	observed, err := restate.Run(ctx, func(runCtx restate.RunContext) (vpc.ObservedState, error) {
		obs, runErr := lookupVPC(runCtx, api, filter)
		if runErr != nil {
			return obs, classifyLookupError(runErr, vpc.IsNotFound)
		}
		return obs, nil
	})
	if err != nil {
		return nil, err
	}
	if !matchesVPCFilter(observed, filter) {
		return nil, restate.TerminalError(fmt.Errorf("data source lookup: no VPC found matching filter"), 404)
	}
	outputs, err := a.NormalizeOutputs(vpc.VPCOutputs{
		VpcId:              observed.VpcId,
		ARN:                vpcARN(filter.Region, observed.OwnerId, observed.VpcId),
		CidrBlock:          observed.CidrBlock,
		State:              observed.State,
		EnableDnsHostnames: observed.EnableDnsHostnames,
		EnableDnsSupport:   observed.EnableDnsSupport,
		InstanceTenancy:    observed.InstanceTenancy,
		OwnerId:            observed.OwnerId,
		DhcpOptionsId:      observed.DhcpOptionsId,
		IsDefault:          observed.IsDefault,
	})
	if err != nil {
		return nil, restate.TerminalError(err, 500)
	}
	return outputs, nil
}

// DefaultTimeouts provides per-kind default timeouts for VPCs.
func (a *VPCAdapter) DefaultTimeouts() types.ResourceTimeouts {
	return types.ResourceTimeouts{Create: "5m", Update: "5m", Delete: "10m"}
}

// Observe performs a lightweight live check to determine whether the VPC
// exists and matches the desired spec. Implements the Observer interface.
func (a *VPCAdapter) Observe(ctx restate.Context, key string, account string, spec any) (ObserveResult, error) {
	desired, err := castSpec[vpc.VPCSpec](spec)
	if err != nil {
		return ObserveResult{}, err
	}
	// VPC needs the AWS VPC ID to describe; fetch from stored outputs.
	outputs, getErr := restate.Object[vpc.VPCOutputs](ctx, a.ServiceName(), key, "GetOutputs").
		Request(restate.Void{})
	if getErr != nil || outputs.VpcId == "" {
		return ObserveResult{Exists: false}, nil
	}
	api, err := a.planningAPI(ctx, account)
	if err != nil {
		return ObserveResult{}, err
	}
	type describeResult struct {
		State vpc.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(rc restate.RunContext) (describeResult, error) {
		obs, descErr := api.DescribeVpc(rc, outputs.VpcId)
		if descErr != nil {
			if vpc.IsNotFound(descErr) {
				return describeResult{Found: false}, nil
			}
			return describeResult{}, descErr
		}
		return describeResult{State: obs, Found: true}, nil
	})
	if err != nil {
		return ObserveResult{}, err
	}
	if !result.Found {
		return ObserveResult{Exists: false}, nil
	}
	upToDate := !vpc.HasDrift(desired, result.State)
	normalizedOutputs, _ := a.NormalizeOutputs(outputs)
	return ObserveResult{Exists: true, UpToDate: upToDate, Outputs: normalizedOutputs}, nil
}

// decodeSpec unmarshals the raw JSON spec from a resource document into
// the typed VPC spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *VPCAdapter) decodeSpec(doc resourceDocument) (vpc.VPCSpec, error) {
	var spec vpc.VPCSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return vpc.VPCSpec{}, fmt.Errorf("decode VPC spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return vpc.VPCSpec{}, fmt.Errorf("VPC metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return vpc.VPCSpec{}, fmt.Errorf("VPC spec.region is required")
	}
	if strings.TrimSpace(spec.CidrBlock) == "" {
		return vpc.VPCSpec{}, fmt.Errorf("VPC spec.cidrBlock is required")
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	if spec.Tags["Name"] == "" {
		spec.Tags["Name"] = name
	}
	if spec.InstanceTenancy == "" {
		spec.InstanceTenancy = "default"
	}
	spec.Account = ""
	return spec, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *VPCAdapter) planningAPI(ctx restate.Context, account string) (vpc.VPCAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("VPC adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve VPC planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}

// lookupAPI returns the AWS API client used for Lookup (data-source) queries.
// Like planningAPI, it resolves credentials per-account, but also overrides
// the region when the lookup filter specifies one.
func (a *VPCAdapter) lookupAPI(ctx restate.Context, account string, region string) (vpc.VPCAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("VPC adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve VPC planning account %q: %w", account, err)
	}
	if strings.TrimSpace(region) != "" {
		awsCfg.Region = strings.TrimSpace(region)
	}
	return a.apiFactory(awsCfg), nil
}

func lookupVPC(ctx restate.RunContext, api vpc.VPCAPI, filter LookupFilter) (vpc.ObservedState, error) {
	if strings.TrimSpace(filter.ID) != "" {
		return api.DescribeVpc(ctx, strings.TrimSpace(filter.ID))
	}
	tags := lookupTags(filter)
	if len(tags) == 0 {
		return vpc.ObservedState{}, fmt.Errorf("VPC lookup requires at least one of: id, name, tag")
	}
	id, err := api.FindByTags(ctx, tags)
	if err != nil {
		return vpc.ObservedState{}, err
	}
	if strings.TrimSpace(id) == "" {
		return vpc.ObservedState{}, fmt.Errorf("not found")
	}
	return api.DescribeVpc(ctx, id)
}

func matchesVPCFilter(observed vpc.ObservedState, filter LookupFilter) bool {
	if strings.TrimSpace(filter.ID) != "" && observed.VpcId != strings.TrimSpace(filter.ID) {
		return false
	}
	if strings.TrimSpace(filter.Name) != "" && observed.Tags["Name"] != strings.TrimSpace(filter.Name) {
		return false
	}
	for key, value := range filter.Tag {
		if observed.Tags[key] != value {
			return false
		}
	}
	return true
}

func vpcARN(region, ownerID, vpcID string) string {
	if strings.TrimSpace(region) == "" || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(vpcID) == "" {
		return ""
	}
	return fmt.Sprintf("arn:aws:ec2:%s:%s:vpc/%s", region, ownerID, vpcID)
}
