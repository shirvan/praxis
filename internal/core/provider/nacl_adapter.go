// NetworkACL provider adapter — descriptor for the GenericAdapter.
//
// Key scope: custom (VPC-scoped).
// Key parts: VPC ID + ACL name.
// Network ACLs are scoped to a VPC, so the key combines the VPC ID and ACL name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/nacl"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// NetworkACLAdapter is the descriptor-driven adapter for NetworkACL.
type NetworkACLAdapter = GenericAdapter[nacl.NetworkACLSpec, nacl.NetworkACLOutputs, nacl.ObservedState]

func naclDescriptor() GenericDescriptor[nacl.NetworkACLSpec, nacl.NetworkACLOutputs, nacl.ObservedState] {
	return GenericDescriptor[nacl.NetworkACLSpec, nacl.NetworkACLOutputs, nacl.ObservedState]{
		Kind:  nacl.ServiceName,
		Scope: KeyScopeCustom,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (nacl.NetworkACLSpec, error) {
			var parsed nacl.NetworkACLSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return nacl.NetworkACLSpec{}, fmt.Errorf("decode NetworkACL spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return nacl.NetworkACLSpec{}, fmt.Errorf("NetworkACL metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return nacl.NetworkACLSpec{}, fmt.Errorf("NetworkACL spec.region is required")
			}
			if strings.TrimSpace(parsed.VpcId) == "" {
				return nacl.NetworkACLSpec{}, fmt.Errorf("NetworkACL spec.vpcId is required")
			}
			if parsed.Tags == nil {
				parsed.Tags = make(map[string]string)
			}
			if parsed.Tags["Name"] == "" {
				parsed.Tags["Name"] = name
			}
			parsed.Account = ""
			return parsed, nil
		},

		KeyFromSpec: func(spec nacl.NetworkACLSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("VPC ID", spec.VpcId); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("network ACL name", name); err != nil {
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

		PrepareSpec: func(spec nacl.NetworkACLSpec, key, account string) nacl.NetworkACLSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out nacl.NetworkACLOutputs) map[string]any {
			return map[string]any{
				"networkAclId": out.NetworkAclId,
				"vpcId":        out.VpcId,
				"isDefault":    out.IsDefault,
				"ingressRules": out.IngressRules,
				"egressRules":  out.EgressRules,
				"associations": out.Associations,
			}
		},

		PlanID: func(out nacl.NetworkACLOutputs) string { return out.NetworkAclId },

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[nacl.ObservedState] {
			return naclProbe(nacl.NewNetworkACLAPI(awsclient.NewEC2Client(cfg)))
		},

		DiffFields: func(desired nacl.NetworkACLSpec, observed nacl.ObservedState) []types.FieldDiff {
			rawDiffs := nacl.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// naclProbe adapts the driver API to the generic plan probe shape.
func naclProbe(api nacl.NetworkACLAPI) PlanProbeFunc[nacl.ObservedState] {
	return func(runCtx restate.RunContext, networkACLID string) (nacl.ObservedState, bool, error) {
		obs, err := api.DescribeNetworkACL(runCtx, networkACLID)
		if err != nil {
			if nacl.IsNotFound(err) {
				return nacl.ObservedState{}, false, nil
			}
			return nacl.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewNetworkACLAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewNetworkACLAdapterWithAuth(auth authservice.AuthClient) *NetworkACLAdapter {
	return NewGenericAdapter(naclDescriptor(), auth)
}

// NewNetworkACLAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewNetworkACLAdapterWithAPI(api nacl.NetworkACLAPI) *NetworkACLAdapter {
	return NewGenericAdapterWithProbe(naclDescriptor(), naclProbe(api))
}
