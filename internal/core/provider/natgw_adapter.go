// NATGateway provider adapter — descriptor for the GenericAdapter.
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

// NATGatewayAdapter is the descriptor-driven adapter for NATGateway, extended
// with per-kind default timeouts.
type NATGatewayAdapter struct {
	*GenericAdapter[natgw.NATGatewaySpec, natgw.NATGatewayOutputs, natgw.ObservedState]
}

func natgwDescriptor() GenericDescriptor[natgw.NATGatewaySpec, natgw.NATGatewayOutputs, natgw.ObservedState] {
	return GenericDescriptor[natgw.NATGatewaySpec, natgw.NATGatewayOutputs, natgw.ObservedState]{
		Kind:  natgw.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (natgw.NATGatewaySpec, error) {
			var spec natgw.NATGatewaySpec
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return natgw.NATGatewaySpec{}, fmt.Errorf("decode NATGateway spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
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
		},

		KeyFromSpec: func(spec natgw.NATGatewaySpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("NAT gateway name", name); err != nil {
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

		PrepareSpec: func(spec natgw.NATGatewaySpec, key, account string) natgw.NATGatewaySpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out natgw.NATGatewayOutputs) map[string]any {
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
			return result
		},

		PlanIdentity: storedPlanIdentity[natgw.NATGatewaySpec](func(out natgw.NATGatewayOutputs) string { return out.NatGatewayId }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[natgw.NATGatewaySpec, natgw.NATGatewayOutputs, natgw.ObservedState] {
			return natgwProbe(natgw.NewNATGatewayAPI(awsclient.NewEC2Client(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[natgw.NATGatewayOutputs] {
			return natgwLookupProbe(natgw.NewNATGatewayAPI(awsclient.NewEC2Client(cfg)))
		},

		DiffFields: func(desired natgw.NATGatewaySpec, observed natgw.ObservedState, _ natgw.NATGatewayOutputs) []types.FieldDiff {
			return natgw.ComputeFieldDiffs(desired, observed)
		},
	}
}

func natgwLookupProbe(api natgw.NATGatewayAPI) LookupProbeFunc[natgw.NATGatewayOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (natgw.NATGatewayOutputs, bool, error) {
		natGatewayID := strings.TrimSpace(filter.ID)
		if natGatewayID == "" {
			return natgw.NATGatewayOutputs{}, false, restate.TerminalError(
				fmt.Errorf("NATGateway lookup supports id; name-only and tag-only lookup are not available"),
				400,
			)
		}
		observed, err := api.DescribeNATGateway(ctx, natGatewayID)
		if err != nil {
			if isLookupNotFound(err, natgw.IsNotFound) {
				return natgw.NATGatewayOutputs{}, false, nil
			}
			return natgw.NATGatewayOutputs{}, false, err
		}
		if observed.NatGatewayId != natGatewayID || !matchesLookupTags(observed.Tags, LookupFilter{Name: filter.Name, Tag: filter.Tag}) {
			return natgw.NATGatewayOutputs{}, false, nil
		}
		return natgw.NATGatewayOutputs{
			NatGatewayId: observed.NatGatewayId, SubnetId: observed.SubnetId, VpcId: observed.VpcId,
			ConnectivityType: observed.ConnectivityType, State: observed.State, PublicIp: observed.PublicIp,
			PrivateIp: observed.PrivateIp, AllocationId: observed.AllocationId,
			NetworkInterfaceId: observed.NetworkInterfaceId,
		}, true, nil
	}
}

// natgwProbe adapts the driver API to the generic plan probe shape.
func natgwProbe(api natgw.NATGatewayAPI) PlanProbeFunc[natgw.NATGatewaySpec, natgw.NATGatewayOutputs, natgw.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[natgw.NATGatewaySpec, natgw.NATGatewayOutputs]) (natgw.ObservedState, bool, error) {
		natGatewayID := input.Identity
		obs, err := api.DescribeNATGateway(runCtx, natGatewayID)
		if err != nil {
			if natgw.IsNotFound(err) {
				return natgw.ObservedState{}, false, nil
			}
			return natgw.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewNATGatewayAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewNATGatewayAdapterWithAuth(auth authservice.AuthClient) *NATGatewayAdapter {
	return &NATGatewayAdapter{GenericAdapter: NewGenericAdapter(natgwDescriptor(), auth)}
}

// NewNATGatewayAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewNATGatewayAdapterWithAPI(api natgw.NATGatewayAPI) *NATGatewayAdapter {
	return &NATGatewayAdapter{GenericAdapter: NewGenericAdapterWithProbes(natgwDescriptor(), natgwProbe(api), natgwLookupProbe(api))}
}

// DefaultTimeouts provides per-kind default timeouts for NAT Gateways.
func (a *NATGatewayAdapter) DefaultTimeouts() types.ResourceTimeouts {
	return types.ResourceTimeouts{Create: "10m", Update: "10m", Delete: "10m"}
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
