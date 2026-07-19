// RouteTable provider adapter — descriptor for the GenericAdapter.
//
// Key scope: custom (VPC-scoped).
// Key parts: VPC ID + route table name.
// Route tables are scoped to a VPC, so the key combines the VPC ID and route
// table name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/routetable"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// RouteTableAdapter is the descriptor-driven adapter for RouteTable.
type RouteTableAdapter = GenericAdapter[routetable.RouteTableSpec, routetable.RouteTableOutputs, routetable.ObservedState]

func routeTableDescriptor() GenericDescriptor[routetable.RouteTableSpec, routetable.RouteTableOutputs, routetable.ObservedState] {
	return GenericDescriptor[routetable.RouteTableSpec, routetable.RouteTableOutputs, routetable.ObservedState]{
		Kind:  routetable.ServiceName,
		Scope: KeyScopeCustom,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (routetable.RouteTableSpec, error) {
			var parsed routetable.RouteTableSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return routetable.RouteTableSpec{}, fmt.Errorf("decode RouteTable spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return routetable.RouteTableSpec{}, fmt.Errorf("RouteTable metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return routetable.RouteTableSpec{}, fmt.Errorf("RouteTable spec.region is required")
			}
			if strings.TrimSpace(parsed.VpcId) == "" {
				return routetable.RouteTableSpec{}, fmt.Errorf("RouteTable spec.vpcId is required")
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

		KeyFromSpec: func(spec routetable.RouteTableSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("VPC ID", spec.VpcId); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("route table name", name); err != nil {
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

		PrepareSpec: func(spec routetable.RouteTableSpec, key, account string) routetable.RouteTableSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out routetable.RouteTableOutputs) map[string]any {
			return map[string]any{
				"routeTableId": out.RouteTableId,
				"vpcId":        out.VpcId,
				"ownerId":      out.OwnerId,
				"routes":       out.Routes,
				"associations": out.Associations,
			}
		},

		PlanIdentity: storedPlanIdentity[routetable.RouteTableSpec](func(out routetable.RouteTableOutputs) string { return out.RouteTableId }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[routetable.RouteTableSpec, routetable.RouteTableOutputs, routetable.ObservedState] {
			return routeTableProbe(routetable.NewRouteTableAPI(awsclient.NewEC2Client(cfg)))
		},

		DiffFields: func(desired routetable.RouteTableSpec, observed routetable.ObservedState, _ routetable.RouteTableOutputs) []types.FieldDiff {
			rawDiffs := routetable.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// routeTableProbe adapts the driver API to the generic plan probe shape.
func routeTableProbe(api routetable.RouteTableAPI) PlanProbeFunc[routetable.RouteTableSpec, routetable.RouteTableOutputs, routetable.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[routetable.RouteTableSpec, routetable.RouteTableOutputs]) (routetable.ObservedState, bool, error) {
		routeTableID := input.Identity
		obs, err := api.DescribeRouteTable(runCtx, routeTableID)
		if err != nil {
			if routetable.IsNotFound(err) {
				return routetable.ObservedState{}, false, nil
			}
			return routetable.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewRouteTableAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewRouteTableAdapterWithAuth(auth authservice.AuthClient) *RouteTableAdapter {
	return NewGenericAdapter(routeTableDescriptor(), auth)
}

// NewRouteTableAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewRouteTableAdapterWithAPI(api routetable.RouteTableAPI) *RouteTableAdapter {
	return NewGenericAdapterWithProbe(routeTableDescriptor(), routeTableProbe(api))
}
