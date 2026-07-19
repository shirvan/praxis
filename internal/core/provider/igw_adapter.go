// InternetGateway provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + gateway name.
// Internet gateways are region-scoped; the key combines the AWS region and the Name tag.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/igw"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// IGWAdapter is the descriptor-driven adapter for InternetGateway.
type IGWAdapter = GenericAdapter[igw.IGWSpec, igw.IGWOutputs, igw.ObservedState]

func igwDescriptor() GenericDescriptor[igw.IGWSpec, igw.IGWOutputs, igw.ObservedState] {
	return GenericDescriptor[igw.IGWSpec, igw.IGWOutputs, igw.ObservedState]{
		Kind:  igw.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (igw.IGWSpec, error) {
			var parsed igw.IGWSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return igw.IGWSpec{}, fmt.Errorf("decode InternetGateway spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return igw.IGWSpec{}, fmt.Errorf("InternetGateway metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return igw.IGWSpec{}, fmt.Errorf("InternetGateway spec.region is required")
			}
			if strings.TrimSpace(parsed.VpcId) == "" {
				return igw.IGWSpec{}, fmt.Errorf("InternetGateway spec.vpcId is required")
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

		KeyFromSpec: func(spec igw.IGWSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("internet gateway name", name); err != nil {
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

		PrepareSpec: func(spec igw.IGWSpec, key, account string) igw.IGWSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out igw.IGWOutputs) map[string]any {
			return map[string]any{
				"internetGatewayId": out.InternetGatewayId,
				"vpcId":             out.VpcId,
				"ownerId":           out.OwnerId,
				"state":             out.State,
			}
		},

		PlanIdentity: storedPlanIdentity[igw.IGWSpec](func(out igw.IGWOutputs) string { return out.InternetGatewayId }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[igw.IGWSpec, igw.IGWOutputs, igw.ObservedState] {
			return igwProbe(igw.NewIGWAPI(awsclient.NewEC2Client(cfg)))
		},

		DiffFields: func(desired igw.IGWSpec, observed igw.ObservedState, _ igw.IGWOutputs) []types.FieldDiff {
			return igw.ComputeFieldDiffs(desired, observed)
		},
	}
}

// igwProbe adapts the driver API to the generic plan probe shape.
func igwProbe(api igw.IGWAPI) PlanProbeFunc[igw.IGWSpec, igw.IGWOutputs, igw.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[igw.IGWSpec, igw.IGWOutputs]) (igw.ObservedState, bool, error) {
		gatewayID := input.Identity
		obs, err := api.DescribeInternetGateway(runCtx, gatewayID)
		if err != nil {
			if igw.IsNotFound(err) {
				return igw.ObservedState{}, false, nil
			}
			return igw.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewIGWAdapterWithAuth builds the production adapter; plan-time credentials
// are resolved through the Auth Service.
func NewIGWAdapterWithAuth(auth authservice.AuthClient) *IGWAdapter {
	return NewGenericAdapter(igwDescriptor(), auth)
}

// NewIGWAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewIGWAdapterWithAPI(api igw.IGWAPI) *IGWAdapter {
	return NewGenericAdapterWithProbe(igwDescriptor(), igwProbe(api))
}
