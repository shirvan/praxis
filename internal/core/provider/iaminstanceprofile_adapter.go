// IAMInstanceProfile provider adapter — descriptor for the GenericAdapter.
//
// Key scope: global (IAM is region-free).
// Key parts: instance profile name.
// IAM instance profiles are global; the key is the instance profile name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/iaminstanceprofile"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// IAMInstanceProfileAdapter is the descriptor-driven adapter for IAMInstanceProfile.
type IAMInstanceProfileAdapter = GenericAdapter[iaminstanceprofile.IAMInstanceProfileSpec, iaminstanceprofile.IAMInstanceProfileOutputs, iaminstanceprofile.ObservedState]

func iamInstanceProfileDescriptor() GenericDescriptor[iaminstanceprofile.IAMInstanceProfileSpec, iaminstanceprofile.IAMInstanceProfileOutputs, iaminstanceprofile.ObservedState] {
	return GenericDescriptor[iaminstanceprofile.IAMInstanceProfileSpec, iaminstanceprofile.IAMInstanceProfileOutputs, iaminstanceprofile.ObservedState]{
		Kind:  iaminstanceprofile.ServiceName,
		Scope: KeyScopeGlobal,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (iaminstanceprofile.IAMInstanceProfileSpec, error) {
			var spec struct {
				Path     string            `json:"path"`
				RoleName string            `json:"roleName"`
				Tags     map[string]string `json:"tags"`
			}
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return iaminstanceprofile.IAMInstanceProfileSpec{}, fmt.Errorf("decode IAMInstanceProfile spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return iaminstanceprofile.IAMInstanceProfileSpec{}, fmt.Errorf("IAMInstanceProfile metadata.name is required")
			}
			if strings.TrimSpace(spec.RoleName) == "" {
				return iaminstanceprofile.IAMInstanceProfileSpec{}, fmt.Errorf("IAMInstanceProfile spec.roleName is required")
			}
			if spec.Path == "" {
				spec.Path = "/"
			}
			if spec.Tags == nil {
				spec.Tags = map[string]string{}
			}
			return iaminstanceprofile.IAMInstanceProfileSpec{
				Account:             "",
				Path:                spec.Path,
				InstanceProfileName: name,
				RoleName:            spec.RoleName,
				Tags:                spec.Tags,
			}, nil
		},

		KeyFromSpec: func(spec iaminstanceprofile.IAMInstanceProfileSpec, _ string) (string, error) {
			if err := ValidateKeyPart("instance profile name", spec.InstanceProfileName); err != nil {
				return "", err
			}
			return spec.InstanceProfileName, nil
		},

		ImportKey: func(_, resourceID string) (string, error) {
			if err := ValidateKeyPart("resource ID", resourceID); err != nil {
				return "", err
			}
			return resourceID, nil
		},

		PrepareSpec: func(spec iaminstanceprofile.IAMInstanceProfileSpec, _, account string) iaminstanceprofile.IAMInstanceProfileSpec {
			spec.Account = account
			return spec
		},

		NormalizeOutputs: func(out iaminstanceprofile.IAMInstanceProfileOutputs) map[string]any {
			return map[string]any{
				"arn":                 out.Arn,
				"instanceProfileId":   out.InstanceProfileId,
				"instanceProfileName": out.InstanceProfileName,
			}
		},

		PlanIdentity: storedPlanIdentity[iaminstanceprofile.IAMInstanceProfileSpec](func(out iaminstanceprofile.IAMInstanceProfileOutputs) string { return out.InstanceProfileName }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[iaminstanceprofile.IAMInstanceProfileSpec, iaminstanceprofile.IAMInstanceProfileOutputs, iaminstanceprofile.ObservedState] {
			return iamInstanceProfileProbe(iaminstanceprofile.NewIAMInstanceProfileAPI(awsclient.NewIAMClient(cfg)))
		},

		DiffFields: func(desired iaminstanceprofile.IAMInstanceProfileSpec, observed iaminstanceprofile.ObservedState, _ iaminstanceprofile.IAMInstanceProfileOutputs) []types.FieldDiff {
			return iaminstanceprofile.ComputeFieldDiffs(desired, observed)
		},
	}
}

// iamInstanceProfileProbe adapts the driver API to the generic plan probe shape.
func iamInstanceProfileProbe(api iaminstanceprofile.IAMInstanceProfileAPI) PlanProbeFunc[iaminstanceprofile.IAMInstanceProfileSpec, iaminstanceprofile.IAMInstanceProfileOutputs, iaminstanceprofile.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[iaminstanceprofile.IAMInstanceProfileSpec, iaminstanceprofile.IAMInstanceProfileOutputs]) (iaminstanceprofile.ObservedState, bool, error) {
		profileName := input.Identity
		obs, err := api.DescribeInstanceProfile(runCtx, profileName)
		if err != nil {
			if iaminstanceprofile.IsNotFound(err) {
				return iaminstanceprofile.ObservedState{}, false, nil
			}
			return iaminstanceprofile.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewIAMInstanceProfileAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewIAMInstanceProfileAdapterWithAuth(auth authservice.AuthClient) *IAMInstanceProfileAdapter {
	return NewGenericAdapter(iamInstanceProfileDescriptor(), auth)
}

// NewIAMInstanceProfileAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewIAMInstanceProfileAdapterWithAPI(api iaminstanceprofile.IAMInstanceProfileAPI) *IAMInstanceProfileAdapter {
	return NewGenericAdapterWithProbe(iamInstanceProfileDescriptor(), iamInstanceProfileProbe(api))
}
