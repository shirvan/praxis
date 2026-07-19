// SSMParameter provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + parameter name.
// Parameter names are unique within a region; the key combines the AWS region
// and the (possibly hierarchical, slash-separated) parameter name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/ssmparameter"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// SSMParameterAdapter is the descriptor-driven adapter for SSMParameter.
type SSMParameterAdapter = GenericAdapter[ssmparameter.SSMParameterSpec, ssmparameter.SSMParameterOutputs, ssmparameter.ObservedState]

func ssmParameterDescriptor() GenericDescriptor[ssmparameter.SSMParameterSpec, ssmparameter.SSMParameterOutputs, ssmparameter.ObservedState] {
	return GenericDescriptor[ssmparameter.SSMParameterSpec, ssmparameter.SSMParameterOutputs, ssmparameter.ObservedState]{
		Kind:  ssmparameter.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (ssmparameter.SSMParameterSpec, error) {
			var parsed ssmparameter.SSMParameterSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return ssmparameter.SSMParameterSpec{}, fmt.Errorf("decode SSMParameter spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return ssmparameter.SSMParameterSpec{}, fmt.Errorf("SSMParameter metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return ssmparameter.SSMParameterSpec{}, fmt.Errorf("SSMParameter spec.region is required")
			}
			if parsed.Tags == nil {
				parsed.Tags = map[string]string{}
			}
			parsed.ParameterName = name
			return parsed, nil
		},

		KeyFromSpec: func(spec ssmparameter.SSMParameterSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("parameter name", name); err != nil {
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

		PrepareSpec: func(spec ssmparameter.SSMParameterSpec, key, account string) ssmparameter.SSMParameterSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out ssmparameter.SSMParameterOutputs) map[string]any {
			result := map[string]any{
				"parameterName": out.ParameterName,
				"type":          out.Type,
				"version":       out.Version,
				"tier":          out.Tier,
			}
			if out.ARN != "" {
				result["arn"] = out.ARN
			}
			if out.DataType != "" {
				result["dataType"] = out.DataType
			}
			return result
		},

		PlanIdentity: storedPlanIdentity[ssmparameter.SSMParameterSpec](func(out ssmparameter.SSMParameterOutputs) string { return out.ParameterName }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[ssmparameter.SSMParameterSpec, ssmparameter.SSMParameterOutputs, ssmparameter.ObservedState] {
			return ssmParameterProbe(ssmparameter.NewSSMParameterAPI(awsclient.NewSSMClient(cfg)))
		},

		DiffFields: func(desired ssmparameter.SSMParameterSpec, observed ssmparameter.ObservedState, _ ssmparameter.SSMParameterOutputs) []types.FieldDiff {
			return ssmparameter.ComputeFieldDiffs(desired, observed)
		},
		// SSM parameter values are masked unconditionally in plan output. The
		// driver's own drift diff masks only SecureString on the update path;
		// the create path has no type context, so we mask defensively — plain
		// String params show "(sensitive)" in plans rather than risk leaking a
		// SecureString value.
		SensitiveFields: []string{"spec.value"},
	}
}

// ssmParameterProbe adapts the driver API to the generic plan probe shape. The
// driver's describe reports existence directly alongside the observed state.
func ssmParameterProbe(api ssmparameter.SSMParameterAPI) PlanProbeFunc[ssmparameter.SSMParameterSpec, ssmparameter.SSMParameterOutputs, ssmparameter.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[ssmparameter.SSMParameterSpec, ssmparameter.SSMParameterOutputs]) (ssmparameter.ObservedState, bool, error) {
		parameterName := input.Identity
		obs, found, err := api.DescribeParameter(runCtx, parameterName)
		if err != nil {
			if ssmparameter.IsNotFound(err) {
				return ssmparameter.ObservedState{}, false, nil
			}
			return ssmparameter.ObservedState{}, false, err
		}
		return obs, found, nil
	}
}

// NewSSMParameterAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewSSMParameterAdapterWithAuth(auth authservice.AuthClient) *SSMParameterAdapter {
	return NewGenericAdapter(ssmParameterDescriptor(), auth)
}

// NewSSMParameterAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewSSMParameterAdapterWithAPI(api ssmparameter.SSMParameterAPI) *SSMParameterAdapter {
	return NewGenericAdapterWithProbe(ssmParameterDescriptor(), ssmParameterProbe(api))
}
