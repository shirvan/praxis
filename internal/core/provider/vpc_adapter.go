// VPC provider adapter — descriptor for the GenericAdapter.
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

// VPCAdapter is the descriptor-driven adapter for VPC, extended with per-kind
// default timeouts, a read-only Lookup for template data sources, and a live
// Observe check. The auth/staticPlanningAPI/apiFactory fields back the Lookup
// and Observe paths; the embedded GenericAdapter owns the plan-time probe.
type VPCAdapter struct {
	*GenericAdapter[vpc.VPCSpec, vpc.VPCOutputs, vpc.ObservedState]
	auth              authservice.AuthClient
	staticPlanningAPI vpc.VPCAPI
	apiFactory        func(aws.Config) vpc.VPCAPI
}

func vpcDescriptor() GenericDescriptor[vpc.VPCSpec, vpc.VPCOutputs, vpc.ObservedState] {
	return GenericDescriptor[vpc.VPCSpec, vpc.VPCOutputs, vpc.ObservedState]{
		Kind:  vpc.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (vpc.VPCSpec, error) {
			var spec vpc.VPCSpec
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return vpc.VPCSpec{}, fmt.Errorf("decode VPC spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
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
		},

		KeyFromSpec: func(spec vpc.VPCSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("VPC name", name); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, name), nil
		},

		ImportKey: func(region, resourceID string) (string, error) {
			if err := ValidateKeyPart("region", region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("resource ID", resourceID); err != nil {
				return "", err
			}
			return JoinKey(region, resourceID), nil
		},

		PrepareSpec: func(spec vpc.VPCSpec, key, account string) vpc.VPCSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: normalizeVPCOutputs,

		PlanID: func(out vpc.VPCOutputs) string { return out.VpcId },

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[vpc.ObservedState] {
			return vpcProbe(vpc.NewVPCAPI(awsclient.NewEC2Client(cfg)))
		},

		DiffFields: func(desired vpc.VPCSpec, observed vpc.ObservedState) []types.FieldDiff {
			rawDiffs := vpc.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// normalizeVPCOutputs converts the typed driver outputs into the generic
// output map. Shared between the descriptor and the Lookup path.
func normalizeVPCOutputs(out vpc.VPCOutputs) map[string]any {
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
	return result
}

// vpcProbe adapts the driver API to the generic plan probe shape.
func vpcProbe(api vpc.VPCAPI) PlanProbeFunc[vpc.ObservedState] {
	return func(runCtx restate.RunContext, vpcID string) (vpc.ObservedState, bool, error) {
		obs, err := api.DescribeVpc(runCtx, vpcID)
		if err != nil {
			if vpc.IsNotFound(err) {
				return vpc.ObservedState{}, false, nil
			}
			return vpc.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewVPCAdapterWithAuth builds the production adapter; plan-time, lookup-time,
// and observe-time credentials are resolved through the Auth Service.
func NewVPCAdapterWithAuth(auth authservice.AuthClient) *VPCAdapter {
	return &VPCAdapter{
		GenericAdapter: NewGenericAdapter(vpcDescriptor(), auth),
		auth:           auth,
		apiFactory: func(cfg aws.Config) vpc.VPCAPI {
			return vpc.NewVPCAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

// NewVPCAdapterWithAPI builds an adapter with a fixed planning/lookup API.
// Used by tests.
func NewVPCAdapterWithAPI(api vpc.VPCAPI) *VPCAdapter {
	return &VPCAdapter{
		GenericAdapter:    NewGenericAdapterWithProbe(vpcDescriptor(), vpcProbe(api)),
		staticPlanningAPI: api,
	}
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
	return normalizeVPCOutputs(vpc.VPCOutputs{
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
	}), nil
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
	return ObserveResult{Exists: true, UpToDate: upToDate, Outputs: normalizeVPCOutputs(outputs)}, nil
}

// planningAPI returns the AWS API client used for read-only describe
// operations (Observe). In production it resolves credentials for the given
// account via the auth client and creates a fresh API. In tests it returns
// the staticPlanningAPI.
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
