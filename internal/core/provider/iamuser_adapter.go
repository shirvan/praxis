// IAMUser provider adapter — descriptor for the GenericAdapter.
//
// Key scope: global (IAM is region-free).
// Key parts: user name.
// IAM users are global; the key is the user name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/iamuser"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// IAMUserAdapter is the descriptor-driven adapter for IAMUser.
type IAMUserAdapter = GenericAdapter[iamuser.IAMUserSpec, iamuser.IAMUserOutputs, iamuser.ObservedState]

func iamUserDescriptor() GenericDescriptor[iamuser.IAMUserSpec, iamuser.IAMUserOutputs, iamuser.ObservedState] {
	return GenericDescriptor[iamuser.IAMUserSpec, iamuser.IAMUserOutputs, iamuser.ObservedState]{
		Kind:  iamuser.ServiceName,
		Scope: KeyScopeGlobal,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (iamuser.IAMUserSpec, error) {
			var spec struct {
				Path                string            `json:"path"`
				PermissionsBoundary string            `json:"permissionsBoundary"`
				InlinePolicies      map[string]string `json:"inlinePolicies"`
				ManagedPolicyArns   []string          `json:"managedPolicyArns"`
				Groups              []string          `json:"groups"`
				Tags                map[string]string `json:"tags"`
			}
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return iamuser.IAMUserSpec{}, fmt.Errorf("decode IAMUser spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return iamuser.IAMUserSpec{}, fmt.Errorf("IAMUser metadata.name is required")
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
			if spec.Groups == nil {
				spec.Groups = []string{}
			}
			if spec.Tags == nil {
				spec.Tags = map[string]string{}
			}
			return iamuser.IAMUserSpec{
				Path:                spec.Path,
				UserName:            name,
				PermissionsBoundary: spec.PermissionsBoundary,
				InlinePolicies:      spec.InlinePolicies,
				ManagedPolicyArns:   spec.ManagedPolicyArns,
				Groups:              spec.Groups,
				Tags:                spec.Tags,
			}, nil
		},

		KeyFromSpec: func(spec iamuser.IAMUserSpec, _ string) (string, error) {
			if err := ValidateKeyPart("user name", spec.UserName); err != nil {
				return "", err
			}
			return spec.UserName, nil
		},

		ImportKey: func(_, resourceID string) (string, error) {
			if err := ValidateKeyPart("resource ID", resourceID); err != nil {
				return "", err
			}
			return resourceID, nil
		},

		PrepareSpec: func(spec iamuser.IAMUserSpec, _, account string) iamuser.IAMUserSpec {
			spec.Account = account
			return spec
		},

		NormalizeOutputs: func(out iamuser.IAMUserOutputs) map[string]any {
			return map[string]any{"arn": out.Arn, "userId": out.UserId, "userName": out.UserName}
		},

		PlanID: func(out iamuser.IAMUserOutputs) string { return out.UserName },

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[iamuser.ObservedState] {
			return iamUserProbe(iamuser.NewIAMUserAPI(awsclient.NewIAMClient(cfg)))
		},

		DiffFields: func(desired iamuser.IAMUserSpec, observed iamuser.ObservedState) []types.FieldDiff {
			rawDiffs := iamuser.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// iamUserProbe adapts the driver API to the generic plan probe shape.
func iamUserProbe(api iamuser.IAMUserAPI) PlanProbeFunc[iamuser.ObservedState] {
	return func(runCtx restate.RunContext, userName string) (iamuser.ObservedState, bool, error) {
		obs, err := api.DescribeUser(runCtx, userName)
		if err != nil {
			if iamuser.IsNotFound(err) {
				return iamuser.ObservedState{}, false, nil
			}
			return iamuser.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewIAMUserAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewIAMUserAdapterWithAuth(auth authservice.AuthClient) *IAMUserAdapter {
	return NewGenericAdapter(iamUserDescriptor(), auth)
}

// NewIAMUserAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewIAMUserAdapterWithAPI(api iamuser.IAMUserAPI) *IAMUserAdapter {
	return NewGenericAdapterWithProbe(iamUserDescriptor(), iamUserProbe(api))
}
