// Subnet provider adapter — descriptor for the GenericAdapter.
//
// Key scope: custom (VPC-scoped).
// Key parts: VPC ID + subnet name.
// Subnets are scoped to a VPC, so the key combines the VPC ID and subnet name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/subnet"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// SubnetAdapter is the descriptor-driven adapter for Subnet. The embedded
// GenericAdapter owns plan and data-source lookup execution.
type SubnetAdapter struct {
	*GenericAdapter[subnet.SubnetSpec, subnet.SubnetOutputs, subnet.ObservedState]
}

func subnetDescriptor() GenericDescriptor[subnet.SubnetSpec, subnet.SubnetOutputs, subnet.ObservedState] {
	return GenericDescriptor[subnet.SubnetSpec, subnet.SubnetOutputs, subnet.ObservedState]{
		Kind:  subnet.ServiceName,
		Scope: KeyScopeCustom,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (subnet.SubnetSpec, error) {
			var spec subnet.SubnetSpec
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return subnet.SubnetSpec{}, fmt.Errorf("decode Subnet spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return subnet.SubnetSpec{}, fmt.Errorf("subnet metadata.name is required")
			}
			if strings.TrimSpace(spec.Region) == "" {
				return subnet.SubnetSpec{}, fmt.Errorf("subnet spec.region is required")
			}
			if strings.TrimSpace(spec.VpcId) == "" {
				return subnet.SubnetSpec{}, fmt.Errorf("subnet spec.vpcId is required")
			}
			if strings.TrimSpace(spec.CidrBlock) == "" {
				return subnet.SubnetSpec{}, fmt.Errorf("subnet spec.cidrBlock is required")
			}
			if strings.TrimSpace(spec.AvailabilityZone) == "" {
				return subnet.SubnetSpec{}, fmt.Errorf("subnet spec.availabilityZone is required")
			}
			if spec.Tags == nil {
				spec.Tags = make(map[string]string)
			}
			if spec.Tags["Name"] == "" {
				spec.Tags["Name"] = name
			}
			spec.Account = ""
			return spec, nil
		},

		KeyFromSpec: func(spec subnet.SubnetSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("VPC ID", spec.VpcId); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("subnet name", name); err != nil {
				return "", err
			}
			return JoinKey(spec.VpcId, name), nil
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

		PrepareSpec: func(spec subnet.SubnetSpec, key, account string) subnet.SubnetSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: normalizeSubnetOutputs,

		PlanIdentity: storedPlanIdentity[subnet.SubnetSpec](func(out subnet.SubnetOutputs) string { return out.SubnetId }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[subnet.SubnetSpec, subnet.SubnetOutputs, subnet.ObservedState] {
			return subnetProbe(subnet.NewSubnetAPI(awsclient.NewEC2Client(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[subnet.SubnetOutputs] {
			return subnetLookupProbe(subnet.NewSubnetAPI(awsclient.NewEC2Client(cfg)))
		},

		DiffFields: func(desired subnet.SubnetSpec, observed subnet.ObservedState, _ subnet.SubnetOutputs) []types.FieldDiff {
			return subnet.ComputeFieldDiffs(desired, observed)
		},
	}
}

// normalizeSubnetOutputs converts the typed driver outputs into the generic
// output map. Shared between the descriptor and the Lookup path.
func normalizeSubnetOutputs(out subnet.SubnetOutputs) map[string]any {
	result := map[string]any{
		"subnetId":            out.SubnetId,
		"vpcId":               out.VpcId,
		"cidrBlock":           out.CidrBlock,
		"availabilityZone":    out.AvailabilityZone,
		"availabilityZoneId":  out.AvailabilityZoneId,
		"mapPublicIpOnLaunch": out.MapPublicIpOnLaunch,
		"state":               out.State,
		"ownerId":             out.OwnerId,
		"availableIpCount":    out.AvailableIpCount,
	}
	if out.ARN != "" {
		result["arn"] = out.ARN
	}
	return result
}

// subnetProbe adapts the driver API to the generic plan probe shape.
func subnetProbe(api subnet.SubnetAPI) PlanProbeFunc[subnet.SubnetSpec, subnet.SubnetOutputs, subnet.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[subnet.SubnetSpec, subnet.SubnetOutputs]) (subnet.ObservedState, bool, error) {
		subnetID := input.Identity
		obs, err := api.DescribeSubnet(runCtx, subnetID)
		if err != nil {
			if subnet.IsNotFound(err) {
				return subnet.ObservedState{}, false, nil
			}
			return subnet.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

func subnetLookupProbe(api subnet.SubnetAPI) LookupProbeFunc[subnet.SubnetOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (subnet.SubnetOutputs, bool, error) {
		observed, err := lookupSubnet(ctx, api, filter)
		if err != nil {
			if isLookupNotFound(err, subnet.IsNotFound) {
				return subnet.SubnetOutputs{}, false, nil
			}
			return subnet.SubnetOutputs{}, false, err
		}
		if !matchesSubnetFilter(observed, filter) {
			return subnet.SubnetOutputs{}, false, nil
		}
		return subnet.SubnetOutputs{
			SubnetId:            observed.SubnetId,
			ARN:                 subnetARN(filter.Region, observed.OwnerId, observed.SubnetId),
			VpcId:               observed.VpcId,
			CidrBlock:           observed.CidrBlock,
			AvailabilityZone:    observed.AvailabilityZone,
			AvailabilityZoneId:  observed.AvailabilityZoneId,
			MapPublicIpOnLaunch: observed.MapPublicIpOnLaunch,
			State:               observed.State,
			OwnerId:             observed.OwnerId,
			AvailableIpCount:    observed.AvailableIpCount,
		}, true, nil
	}
}

// NewSubnetAdapterWithAuth builds the production adapter; plan-time and
// lookup-time credentials are resolved through the Auth Service.
func NewSubnetAdapterWithAuth(auth authservice.AuthClient) *SubnetAdapter {
	return &SubnetAdapter{
		GenericAdapter: NewGenericAdapter(subnetDescriptor(), auth),
	}
}

// NewSubnetAdapterWithAPI builds an adapter with a fixed planning/lookup API.
// Used by tests.
func NewSubnetAdapterWithAPI(api subnet.SubnetAPI) *SubnetAdapter {
	return &SubnetAdapter{
		GenericAdapter: NewGenericAdapterWithProbes(subnetDescriptor(), subnetProbe(api), subnetLookupProbe(api)),
	}
}

func lookupSubnet(ctx restate.RunContext, api subnet.SubnetAPI, filter LookupFilter) (subnet.ObservedState, error) {
	if strings.TrimSpace(filter.ID) != "" {
		return api.DescribeSubnet(ctx, strings.TrimSpace(filter.ID))
	}
	tags := lookupTags(filter)
	if len(tags) == 0 {
		return subnet.ObservedState{}, fmt.Errorf("subnet lookup requires at least one of: id, name, tag")
	}
	id, err := api.FindByTags(ctx, tags)
	if err != nil {
		return subnet.ObservedState{}, err
	}
	if strings.TrimSpace(id) == "" {
		return subnet.ObservedState{}, fmt.Errorf("not found")
	}
	return api.DescribeSubnet(ctx, id)
}

func matchesSubnetFilter(observed subnet.ObservedState, filter LookupFilter) bool {
	if strings.TrimSpace(filter.ID) != "" && observed.SubnetId != strings.TrimSpace(filter.ID) {
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

func subnetARN(region, ownerID, subnetID string) string {
	if strings.TrimSpace(region) == "" || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(subnetID) == "" {
		return ""
	}
	return fmt.Sprintf("arn:aws:ec2:%s:%s:subnet/%s", region, ownerID, subnetID)
}
