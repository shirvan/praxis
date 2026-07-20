// VPCPeeringConnection provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + peering connection name.
// VPC peering connections are region-scoped; the key combines the AWS region
// and user-provided name. Cross-account and cross-region peering are rejected
// by the driver ("not supported yet").
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/vpcpeering"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// VPCPeeringAdapter is the descriptor-driven adapter for VPCPeeringConnection.
type VPCPeeringAdapter = GenericAdapter[vpcpeering.VPCPeeringSpec, vpcpeering.VPCPeeringOutputs, vpcpeering.ObservedState]

func vpcPeeringDescriptor() GenericDescriptor[vpcpeering.VPCPeeringSpec, vpcpeering.VPCPeeringOutputs, vpcpeering.ObservedState] {
	return GenericDescriptor[vpcpeering.VPCPeeringSpec, vpcpeering.VPCPeeringOutputs, vpcpeering.ObservedState]{
		Kind:  vpcpeering.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (vpcpeering.VPCPeeringSpec, error) {
			var parsed vpcpeering.VPCPeeringSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return vpcpeering.VPCPeeringSpec{}, fmt.Errorf("decode VPCPeeringConnection spec: %w", err)
			}
			// AutoAccept defaults to true when the field is absent from the
			// document; a second decode distinguishes absent from explicit false.
			var raw struct {
				AutoAccept *bool `json:"autoAccept"`
			}
			if err := json.Unmarshal(spec, &raw); err != nil {
				return vpcpeering.VPCPeeringSpec{}, fmt.Errorf("decode VPCPeeringConnection defaults: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return vpcpeering.VPCPeeringSpec{}, fmt.Errorf("VPCPeeringConnection metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return vpcpeering.VPCPeeringSpec{}, fmt.Errorf("VPCPeeringConnection spec.region is required")
			}
			if strings.TrimSpace(parsed.RequesterVpcId) == "" {
				return vpcpeering.VPCPeeringSpec{}, fmt.Errorf("VPCPeeringConnection spec.requesterVpcId is required")
			}
			if strings.TrimSpace(parsed.AccepterVpcId) == "" {
				return vpcpeering.VPCPeeringSpec{}, fmt.Errorf("VPCPeeringConnection spec.accepterVpcId is required")
			}
			if parsed.Tags == nil {
				parsed.Tags = make(map[string]string)
			}
			if parsed.Tags["Name"] == "" {
				parsed.Tags["Name"] = name
			}
			if raw.AutoAccept == nil {
				parsed.AutoAccept = true
			}
			parsed.Account = ""
			return parsed, nil
		},

		KeyFromSpec: func(spec vpcpeering.VPCPeeringSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("VPC peering connection name", name); err != nil {
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

		PrepareSpec: func(spec vpcpeering.VPCPeeringSpec, key, account string) vpcpeering.VPCPeeringSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out vpcpeering.VPCPeeringOutputs) map[string]any {
			return map[string]any{
				"vpcPeeringConnectionId": out.VpcPeeringConnectionId,
				"requesterVpcId":         out.RequesterVpcId,
				"accepterVpcId":          out.AccepterVpcId,
				"requesterCidrBlock":     out.RequesterCidrBlock,
				"accepterCidrBlock":      out.AccepterCidrBlock,
				"status":                 out.Status,
				"requesterOwnerId":       out.RequesterOwnerId,
				"accepterOwnerId":        out.AccepterOwnerId,
			}
		},

		PlanIdentity: storedPlanIdentity[vpcpeering.VPCPeeringSpec](func(out vpcpeering.VPCPeeringOutputs) string { return out.VpcPeeringConnectionId }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[vpcpeering.VPCPeeringSpec, vpcpeering.VPCPeeringOutputs, vpcpeering.ObservedState] {
			return vpcPeeringProbe(vpcpeering.NewVPCPeeringAPI(awsclient.NewEC2Client(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[vpcpeering.VPCPeeringOutputs] {
			return vpcPeeringLookupProbe(vpcpeering.NewVPCPeeringAPI(awsclient.NewEC2Client(cfg)))
		},

		DiffFields: func(desired vpcpeering.VPCPeeringSpec, observed vpcpeering.ObservedState, _ vpcpeering.VPCPeeringOutputs) []types.FieldDiff {
			return vpcpeering.ComputeFieldDiffs(desired, observed)
		},
	}
}

func vpcPeeringLookupProbe(api vpcpeering.VPCPeeringAPI) LookupProbeFunc[vpcpeering.VPCPeeringOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (vpcpeering.VPCPeeringOutputs, bool, error) {
		peeringID := strings.TrimSpace(filter.ID)
		if peeringID == "" {
			return vpcpeering.VPCPeeringOutputs{}, false, restate.TerminalError(
				fmt.Errorf("VPCPeeringConnection lookup supports id; name-only and tag-only lookup are not available"),
				400,
			)
		}
		observed, err := api.DescribeVPCPeeringConnection(ctx, peeringID)
		if err != nil {
			if isLookupNotFound(err, vpcpeering.IsNotFound) {
				return vpcpeering.VPCPeeringOutputs{}, false, nil
			}
			return vpcpeering.VPCPeeringOutputs{}, false, err
		}
		if observed.VpcPeeringConnectionId != peeringID || !matchesLookupTags(observed.Tags, LookupFilter{Name: filter.Name, Tag: filter.Tag}) {
			return vpcpeering.VPCPeeringOutputs{}, false, nil
		}
		return vpcpeering.VPCPeeringOutputs{
			VpcPeeringConnectionId: observed.VpcPeeringConnectionId,
			RequesterVpcId:         observed.RequesterVpcId, AccepterVpcId: observed.AccepterVpcId,
			RequesterCidrBlock: observed.RequesterCidrBlock, AccepterCidrBlock: observed.AccepterCidrBlock,
			Status: observed.Status, RequesterOwnerId: observed.RequesterOwnerId,
			AccepterOwnerId: observed.AccepterOwnerId,
		}, true, nil
	}
}

// vpcPeeringProbe adapts the driver API to the generic plan probe shape.
func vpcPeeringProbe(api vpcpeering.VPCPeeringAPI) PlanProbeFunc[vpcpeering.VPCPeeringSpec, vpcpeering.VPCPeeringOutputs, vpcpeering.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[vpcpeering.VPCPeeringSpec, vpcpeering.VPCPeeringOutputs]) (vpcpeering.ObservedState, bool, error) {
		peeringID := input.Identity
		obs, err := api.DescribeVPCPeeringConnection(runCtx, peeringID)
		if err != nil {
			if vpcpeering.IsNotFound(err) {
				return vpcpeering.ObservedState{}, false, nil
			}
			return vpcpeering.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewVPCPeeringAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewVPCPeeringAdapterWithAuth(auth authservice.AuthClient) *VPCPeeringAdapter {
	return NewGenericAdapter(vpcPeeringDescriptor(), auth)
}

// NewVPCPeeringAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewVPCPeeringAdapterWithAPI(api vpcpeering.VPCPeeringAPI) *VPCPeeringAdapter {
	return NewGenericAdapterWithProbes(vpcPeeringDescriptor(), vpcPeeringProbe(api), vpcPeeringLookupProbe(api))
}
