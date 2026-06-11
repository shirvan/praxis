// ElasticIP provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + elastic IP name.
// Elastic IPs are region-scoped; the key combines the AWS region and the Name tag.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/eip"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// EIPAdapter is the descriptor-driven adapter for ElasticIP.
type EIPAdapter = GenericAdapter[eip.ElasticIPSpec, eip.ElasticIPOutputs, eip.ObservedState]

func eipDescriptor() GenericDescriptor[eip.ElasticIPSpec, eip.ElasticIPOutputs, eip.ObservedState] {
	return GenericDescriptor[eip.ElasticIPSpec, eip.ElasticIPOutputs, eip.ObservedState]{
		Kind:  eip.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (eip.ElasticIPSpec, error) {
			var parsed eip.ElasticIPSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return eip.ElasticIPSpec{}, fmt.Errorf("decode ElasticIP spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return eip.ElasticIPSpec{}, fmt.Errorf("ElasticIP metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return eip.ElasticIPSpec{}, fmt.Errorf("ElasticIP spec.region is required")
			}
			if parsed.Tags == nil {
				parsed.Tags = make(map[string]string)
			}
			if parsed.Tags["Name"] == "" {
				parsed.Tags["Name"] = name
			}
			if parsed.Domain == "" {
				parsed.Domain = "vpc"
			}
			if parsed.Domain != "vpc" {
				return eip.ElasticIPSpec{}, fmt.Errorf("ElasticIP spec.domain must be \"vpc\"")
			}
			parsed.Account = ""
			return parsed, nil
		},

		KeyFromSpec: func(spec eip.ElasticIPSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("elastic IP name", name); err != nil {
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

		PrepareSpec: func(spec eip.ElasticIPSpec, key, account string) eip.ElasticIPSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out eip.ElasticIPOutputs) map[string]any {
			result := map[string]any{
				"allocationId":       out.AllocationId,
				"publicIp":           out.PublicIp,
				"domain":             out.Domain,
				"networkBorderGroup": out.NetworkBorderGroup,
			}
			if out.ARN != "" {
				result["arn"] = out.ARN
			}
			return result
		},

		PlanID: func(out eip.ElasticIPOutputs) string { return out.AllocationId },

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[eip.ObservedState] {
			return eipProbe(eip.NewEIPAPI(awsclient.NewEC2Client(cfg)))
		},

		DiffFields: func(desired eip.ElasticIPSpec, observed eip.ObservedState) []types.FieldDiff {
			rawDiffs := eip.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// eipProbe adapts the driver API to the generic plan probe shape.
func eipProbe(api eip.EIPAPI) PlanProbeFunc[eip.ObservedState] {
	return func(runCtx restate.RunContext, allocationID string) (eip.ObservedState, bool, error) {
		obs, err := api.DescribeAddress(runCtx, allocationID)
		if err != nil {
			if eip.IsNotFound(err) {
				return eip.ObservedState{}, false, nil
			}
			return eip.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewEIPAdapterWithAuth builds the production adapter; plan-time credentials
// are resolved through the Auth Service.
func NewEIPAdapterWithAuth(auth authservice.AuthClient) *EIPAdapter {
	return NewGenericAdapter(eipDescriptor(), auth)
}

// NewEIPAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewEIPAdapterWithAPI(api eip.EIPAPI) *EIPAdapter {
	return NewGenericAdapterWithProbe(eipDescriptor(), eipProbe(api))
}
