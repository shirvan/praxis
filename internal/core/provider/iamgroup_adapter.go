// IAMGroup provider adapter — descriptor for the GenericAdapter.
//
// Key scope: global (IAM is region-free).
// Key parts: group name.
// IAM groups are global; the key is the group name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/iamgroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// IAMGroupAdapter is the descriptor-driven adapter for IAMGroup.
type IAMGroupAdapter = GenericAdapter[iamgroup.IAMGroupSpec, iamgroup.IAMGroupOutputs, iamgroup.ObservedState]

func iamGroupDescriptor() GenericDescriptor[iamgroup.IAMGroupSpec, iamgroup.IAMGroupOutputs, iamgroup.ObservedState] {
	return GenericDescriptor[iamgroup.IAMGroupSpec, iamgroup.IAMGroupOutputs, iamgroup.ObservedState]{
		Kind:  iamgroup.ServiceName,
		Scope: KeyScopeGlobal,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (iamgroup.IAMGroupSpec, error) {
			var spec struct {
				Path              string            `json:"path"`
				InlinePolicies    map[string]string `json:"inlinePolicies"`
				ManagedPolicyArns []string          `json:"managedPolicyArns"`
			}
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return iamgroup.IAMGroupSpec{}, fmt.Errorf("decode IAMGroup spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return iamgroup.IAMGroupSpec{}, fmt.Errorf("IAMGroup metadata.name is required")
			}
			if spec.Path == "" {
				spec.Path = "/"
			}
			if spec.InlinePolicies == nil {
				spec.InlinePolicies = map[string]string{}
			}
			if spec.ManagedPolicyArns == nil {
				spec.ManagedPolicyArns = []string{}
			}
			return iamgroup.IAMGroupSpec{Path: spec.Path, GroupName: name, InlinePolicies: spec.InlinePolicies, ManagedPolicyArns: spec.ManagedPolicyArns}, nil
		},

		KeyFromSpec: func(spec iamgroup.IAMGroupSpec, _ string) (string, error) {
			if err := ValidateKeyPart("group name", spec.GroupName); err != nil {
				return "", err
			}
			return spec.GroupName, nil
		},

		ImportKey: func(_, resourceID string) (string, error) {
			if err := ValidateKeyPart("resource ID", resourceID); err != nil {
				return "", err
			}
			return resourceID, nil
		},

		PrepareSpec: func(spec iamgroup.IAMGroupSpec, _, account string) iamgroup.IAMGroupSpec {
			spec.Account = account
			return spec
		},

		NormalizeOutputs: func(out iamgroup.IAMGroupOutputs) map[string]any {
			return map[string]any{"arn": out.Arn, "groupId": out.GroupId, "groupName": out.GroupName}
		},

		PlanID: func(out iamgroup.IAMGroupOutputs) string { return out.GroupName },

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[iamgroup.ObservedState] {
			return iamGroupProbe(iamgroup.NewIAMGroupAPI(awsclient.NewIAMClient(cfg)))
		},

		DiffFields: func(desired iamgroup.IAMGroupSpec, observed iamgroup.ObservedState) []types.FieldDiff {
			rawDiffs := iamgroup.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// iamGroupProbe adapts the driver API to the generic plan probe shape.
func iamGroupProbe(api iamgroup.IAMGroupAPI) PlanProbeFunc[iamgroup.ObservedState] {
	return func(runCtx restate.RunContext, groupName string) (iamgroup.ObservedState, bool, error) {
		obs, err := api.DescribeGroup(runCtx, groupName)
		if err != nil {
			if iamgroup.IsNotFound(err) {
				return iamgroup.ObservedState{}, false, nil
			}
			return iamgroup.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewIAMGroupAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewIAMGroupAdapterWithAuth(auth authservice.AuthClient) *IAMGroupAdapter {
	return NewGenericAdapter(iamGroupDescriptor(), auth)
}

// NewIAMGroupAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewIAMGroupAdapterWithAPI(api iamgroup.IAMGroupAPI) *IAMGroupAdapter {
	return NewGenericAdapterWithProbe(iamGroupDescriptor(), iamGroupProbe(api))
}
