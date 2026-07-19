// TargetGroup provider adapter — descriptor for the GenericAdapter.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/targetgroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type TargetGroupAdapter = GenericAdapter[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs, targetgroup.ObservedState]

func targetGroupDescriptor() GenericDescriptor[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs, targetgroup.ObservedState] {
	return GenericDescriptor[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs, targetgroup.ObservedState]{
		Kind:  targetgroup.ServiceName,
		Scope: KeyScopeRegion,
		DecodeSpec: func(raw json.RawMessage, metadataName string) (targetgroup.TargetGroupSpec, error) {
			var spec targetgroup.TargetGroupSpec
			if err := json.Unmarshal(raw, &spec); err != nil {
				return targetgroup.TargetGroupSpec{}, fmt.Errorf("decode TargetGroup spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return targetgroup.TargetGroupSpec{}, fmt.Errorf("TargetGroup metadata.name is required")
			}
			if strings.TrimSpace(spec.Region) == "" {
				return targetgroup.TargetGroupSpec{}, fmt.Errorf("TargetGroup spec.region is required")
			}
			spec.Name = name
			spec.Account = ""
			if spec.Tags == nil {
				spec.Tags = map[string]string{}
			}
			if spec.Tags["Name"] == "" {
				spec.Tags["Name"] = name
			}
			return spec, nil
		},
		KeyFromSpec: func(spec targetgroup.TargetGroupSpec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("target group name", spec.Name); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, spec.Name), nil
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
		PrepareSpec: func(spec targetgroup.TargetGroupSpec, _ string, account string) targetgroup.TargetGroupSpec {
			spec.Account = account
			return spec
		},
		NormalizeOutputs: func(out targetgroup.TargetGroupOutputs) map[string]any {
			return map[string]any{"targetGroupArn": out.TargetGroupArn, "targetGroupName": out.TargetGroupName}
		},
		PlanIdentity: func(desired targetgroup.TargetGroupSpec, outputs targetgroup.TargetGroupOutputs) (string, bool) {
			if outputs.TargetGroupArn != "" {
				return outputs.TargetGroupArn, true
			}
			return desired.Name, desired.Name != ""
		},
		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs, targetgroup.ObservedState] {
			return targetGroupProbe(targetgroup.NewTargetGroupAPI(awsclient.NewELBv2Client(cfg)))
		},
		DiffFields: func(desired targetgroup.TargetGroupSpec, observed targetgroup.ObservedState, _ targetgroup.TargetGroupOutputs) []types.FieldDiff {
			return targetgroup.ComputeFieldDiffs(desired, observed)
		},
	}
}

func targetGroupProbe(api targetgroup.TargetGroupAPI) PlanProbeFunc[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs, targetgroup.ObservedState] {
	return func(ctx restate.RunContext, input PlanProbeInput[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs]) (targetgroup.ObservedState, bool, error) {
		observed, err := api.DescribeTargetGroup(ctx, input.Identity)
		if err != nil {
			if targetgroup.IsNotFound(err) {
				return targetgroup.ObservedState{}, false, nil
			}
			return targetgroup.ObservedState{}, false, err
		}
		return observed, true, nil
	}
}

func NewTargetGroupAdapterWithAuth(auth authservice.AuthClient) *TargetGroupAdapter {
	return NewGenericAdapter(targetGroupDescriptor(), auth)
}

func NewTargetGroupAdapterWithAPI(api targetgroup.TargetGroupAPI) *TargetGroupAdapter {
	return NewGenericAdapterWithProbe(targetGroupDescriptor(), targetGroupProbe(api))
}
