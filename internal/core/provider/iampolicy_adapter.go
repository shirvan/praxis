// IAMPolicy provider adapter — descriptor for the GenericAdapter.
//
// Key scope: global (IAM is region-free).
// Key parts: policy name (optionally with path prefix).
// IAM policies are global; the key is derived from the policy name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/iampolicy"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// IAMPolicyAdapter is the descriptor-driven adapter for IAMPolicy.
type IAMPolicyAdapter = GenericAdapter[iampolicy.IAMPolicySpec, iampolicy.IAMPolicyOutputs, iampolicy.ObservedState]

func iamPolicyDescriptor() GenericDescriptor[iampolicy.IAMPolicySpec, iampolicy.IAMPolicyOutputs, iampolicy.ObservedState] {
	return GenericDescriptor[iampolicy.IAMPolicySpec, iampolicy.IAMPolicyOutputs, iampolicy.ObservedState]{
		Kind:  iampolicy.ServiceName,
		Scope: KeyScopeGlobal,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (iampolicy.IAMPolicySpec, error) {
			var spec struct {
				Path           string            `json:"path"`
				PolicyDocument string            `json:"policyDocument"`
				Description    string            `json:"description"`
				Tags           map[string]string `json:"tags"`
			}
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return iampolicy.IAMPolicySpec{}, fmt.Errorf("decode IAMPolicy spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return iampolicy.IAMPolicySpec{}, fmt.Errorf("IAMPolicy metadata.name is required")
			}
			if strings.TrimSpace(spec.PolicyDocument) == "" {
				return iampolicy.IAMPolicySpec{}, fmt.Errorf("IAMPolicy spec.policyDocument is required")
			}
			if spec.Path == "" {
				spec.Path = "/"
			}
			if spec.Tags == nil {
				spec.Tags = map[string]string{}
			}
			return iampolicy.IAMPolicySpec{Path: spec.Path, PolicyName: name, PolicyDocument: spec.PolicyDocument, Description: spec.Description, Tags: spec.Tags}, nil
		},

		KeyFromSpec: func(spec iampolicy.IAMPolicySpec, _ string) (string, error) {
			if err := ValidateKeyPart("policy name", spec.PolicyName); err != nil {
				return "", err
			}
			return spec.PolicyName, nil
		},

		ImportKey: func(_, resourceID string) (string, error) {
			if err := ValidateKeyPart("resource ID", resourceID); err != nil {
				return "", err
			}
			return resourceID, nil
		},

		PrepareSpec: func(spec iampolicy.IAMPolicySpec, _, account string) iampolicy.IAMPolicySpec {
			spec.Account = account
			return spec
		},

		NormalizeOutputs: func(out iampolicy.IAMPolicyOutputs) map[string]any {
			return map[string]any{"arn": out.Arn, "policyId": out.PolicyId, "policyName": out.PolicyName}
		},

		PlanIdentity: storedPlanIdentity[iampolicy.IAMPolicySpec](func(out iampolicy.IAMPolicyOutputs) string { return out.Arn }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[iampolicy.IAMPolicySpec, iampolicy.IAMPolicyOutputs, iampolicy.ObservedState] {
			return iamPolicyProbe(iampolicy.NewIAMPolicyAPI(awsclient.NewIAMClient(cfg)))
		},

		DiffFields: func(desired iampolicy.IAMPolicySpec, observed iampolicy.ObservedState, _ iampolicy.IAMPolicyOutputs) []types.FieldDiff {
			rawDiffs := iampolicy.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// iamPolicyProbe adapts the driver API to the generic plan probe shape. The
// plan ID is the policy ARN recorded in outputs at provision time.
func iamPolicyProbe(api iampolicy.IAMPolicyAPI) PlanProbeFunc[iampolicy.IAMPolicySpec, iampolicy.IAMPolicyOutputs, iampolicy.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[iampolicy.IAMPolicySpec, iampolicy.IAMPolicyOutputs]) (iampolicy.ObservedState, bool, error) {
		policyArn := input.Identity
		obs, err := api.DescribePolicy(runCtx, policyArn)
		if err != nil {
			if iampolicy.IsNotFound(err) {
				return iampolicy.ObservedState{}, false, nil
			}
			return iampolicy.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewIAMPolicyAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewIAMPolicyAdapterWithAuth(auth authservice.AuthClient) *IAMPolicyAdapter {
	return NewGenericAdapter(iamPolicyDescriptor(), auth)
}

// NewIAMPolicyAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewIAMPolicyAdapterWithAPI(api iampolicy.IAMPolicyAPI) *IAMPolicyAdapter {
	return NewGenericAdapterWithProbe(iamPolicyDescriptor(), iamPolicyProbe(api))
}
