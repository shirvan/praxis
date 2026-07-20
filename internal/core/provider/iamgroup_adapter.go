// IAMGroup provider adapter — descriptor for the GenericAdapter.
//
// Key scope: global (IAM is region-free).
// Key parts: group name.
// IAM groups are global; the key is the group name.
package provider

import (
	"encoding/json"
	"fmt"
	"path"
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

		PlanIdentity: storedPlanIdentity[iamgroup.IAMGroupSpec](func(out iamgroup.IAMGroupOutputs) string { return out.GroupName }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[iamgroup.IAMGroupSpec, iamgroup.IAMGroupOutputs, iamgroup.ObservedState] {
			return iamGroupProbe(iamgroup.NewIAMGroupAPI(awsclient.NewIAMClient(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[iamgroup.IAMGroupOutputs] {
			return iamGroupLookupProbe(iamgroup.NewIAMGroupAPI(awsclient.NewIAMClient(cfg)))
		},

		DiffFields: func(desired iamgroup.IAMGroupSpec, observed iamgroup.ObservedState, _ iamgroup.IAMGroupOutputs) []types.FieldDiff {
			return iamgroup.ComputeFieldDiffs(desired, observed)
		},
	}
}

func iamGroupLookupProbe(api iamgroup.IAMGroupAPI) LookupProbeFunc[iamgroup.IAMGroupOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (iamgroup.IAMGroupOutputs, bool, error) {
		if len(filter.Tag) > 0 {
			return iamgroup.IAMGroupOutputs{}, false, restate.TerminalError(
				fmt.Errorf("IAMGroup lookup does not support tag filters"),
				400,
			)
		}
		groupName := iamGroupLookupName(filter)
		if groupName == "" {
			return iamgroup.IAMGroupOutputs{}, false, restate.TerminalError(
				fmt.Errorf("IAMGroup lookup supports id or name"),
				400,
			)
		}
		observed, err := api.DescribeGroup(ctx, groupName)
		if err != nil {
			if isLookupNotFound(err, iamgroup.IsNotFound) {
				return iamgroup.IAMGroupOutputs{}, false, nil
			}
			return iamgroup.IAMGroupOutputs{}, false, err
		}
		if id := strings.TrimSpace(filter.ID); id != "" && observed.Arn != id && observed.GroupId != id && observed.GroupName != id {
			return iamgroup.IAMGroupOutputs{}, false, nil
		}
		if name := strings.TrimSpace(filter.Name); name != "" && observed.GroupName != name {
			return iamgroup.IAMGroupOutputs{}, false, nil
		}
		return iamgroup.IAMGroupOutputs{
			Arn:       observed.Arn,
			GroupId:   observed.GroupId,
			GroupName: observed.GroupName,
		}, true, nil
	}
}

func iamGroupLookupName(filter LookupFilter) string {
	identity := nativeLookupIdentity(filter)
	if strings.Contains(identity, ":group/") {
		return path.Base(identity)
	}
	return identity
}

// iamGroupProbe adapts the driver API to the generic plan probe shape.
func iamGroupProbe(api iamgroup.IAMGroupAPI) PlanProbeFunc[iamgroup.IAMGroupSpec, iamgroup.IAMGroupOutputs, iamgroup.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[iamgroup.IAMGroupSpec, iamgroup.IAMGroupOutputs]) (iamgroup.ObservedState, bool, error) {
		groupName := input.Identity
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
	return NewGenericAdapterWithProbes(iamGroupDescriptor(), iamGroupProbe(api), iamGroupLookupProbe(api))
}
