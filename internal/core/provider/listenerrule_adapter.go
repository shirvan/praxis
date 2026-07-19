// ListenerRule provider adapter — descriptor for the GenericAdapter.
//
// Key scope: custom.
// Key parts: listener ARN + rule priority.
// Listener rules are scoped to a listener; the key combines the listener ARN and rule priority.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/listenerrule"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// ListenerRuleAdapter is the descriptor-driven adapter for ListenerRule.
type ListenerRuleAdapter = GenericAdapter[listenerrule.ListenerRuleSpec, listenerrule.ListenerRuleOutputs, listenerrule.ObservedState]

func listenerRuleDescriptor() GenericDescriptor[listenerrule.ListenerRuleSpec, listenerrule.ListenerRuleOutputs, listenerrule.ObservedState] {
	return GenericDescriptor[listenerrule.ListenerRuleSpec, listenerrule.ListenerRuleOutputs, listenerrule.ObservedState]{
		Kind:  listenerrule.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (listenerrule.ListenerRuleSpec, error) {
			var spec listenerrule.ListenerRuleSpec
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return listenerrule.ListenerRuleSpec{}, fmt.Errorf("decode ListenerRule spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return listenerrule.ListenerRuleSpec{}, fmt.Errorf("ListenerRule metadata.name is required")
			}
			if spec.ListenerArn == "" {
				return listenerrule.ListenerRuleSpec{}, fmt.Errorf("ListenerRule spec.listenerArn is required")
			}
			spec.Account = ""
			if spec.Tags == nil {
				spec.Tags = map[string]string{}
			}
			if spec.Tags["praxis:rule-name"] == "" {
				spec.Tags["praxis:rule-name"] = name
			}
			return spec, nil
		},

		KeyFromSpec: func(spec listenerrule.ListenerRuleSpec, metadataName string) (string, error) {
			region := spec.Region
			if region == "" {
				region = extractRegionFromListenerArn(spec.ListenerArn)
			}
			if region == "" {
				return "", fmt.Errorf("cannot determine region: set spec.region or provide a valid listenerArn")
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("listener rule name", name); err != nil {
				return "", err
			}
			return JoinKey(region, name), nil
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

		PrepareSpec: func(spec listenerrule.ListenerRuleSpec, _ string, account string) listenerrule.ListenerRuleSpec {
			spec.Account = account
			return spec
		},

		NormalizeOutputs: func(out listenerrule.ListenerRuleOutputs) map[string]any {
			return map[string]any{
				"ruleArn":  out.RuleArn,
				"priority": out.Priority,
			}
		},

		PlanIdentity: storedPlanIdentity[listenerrule.ListenerRuleSpec](func(out listenerrule.ListenerRuleOutputs) string { return out.RuleArn }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[listenerrule.ListenerRuleSpec, listenerrule.ListenerRuleOutputs, listenerrule.ObservedState] {
			return listenerRuleProbe(listenerrule.NewListenerRuleAPI(awsclient.NewELBv2Client(cfg)))
		},

		DiffFields: func(desired listenerrule.ListenerRuleSpec, observed listenerrule.ObservedState, _ listenerrule.ListenerRuleOutputs) []types.FieldDiff {
			rawDiffs := listenerrule.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// listenerRuleProbe adapts the driver API to the generic plan probe shape.
func listenerRuleProbe(api listenerrule.ListenerRuleAPI) PlanProbeFunc[listenerrule.ListenerRuleSpec, listenerrule.ListenerRuleOutputs, listenerrule.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[listenerrule.ListenerRuleSpec, listenerrule.ListenerRuleOutputs]) (listenerrule.ObservedState, bool, error) {
		ruleArn := input.Identity
		obs, err := api.DescribeRule(runCtx, ruleArn)
		if err != nil {
			if listenerrule.IsNotFound(err) {
				return listenerrule.ObservedState{}, false, nil
			}
			return listenerrule.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewListenerRuleAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewListenerRuleAdapterWithAuth(auth authservice.AuthClient) *ListenerRuleAdapter {
	return NewGenericAdapter(listenerRuleDescriptor(), auth)
}

// NewListenerRuleAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewListenerRuleAdapterWithAPI(api listenerrule.ListenerRuleAPI) *ListenerRuleAdapter {
	return NewGenericAdapterWithProbe(listenerRuleDescriptor(), listenerRuleProbe(api))
}

func extractRegionFromListenerArn(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 4 {
		return parts[3]
	}
	return ""
}
