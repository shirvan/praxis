// IAMInstanceProfile provider adapter — descriptor for the GenericAdapter.
//
// Key scope: global (IAM is region-free).
// Key parts: instance profile name.
// IAM instance profiles are global; the key is the instance profile name.
package provider

import (
	"encoding/json"
	"fmt"
	"path"
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
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[iaminstanceprofile.IAMInstanceProfileOutputs] {
			return iamInstanceProfileLookupProbe(iaminstanceprofile.NewIAMInstanceProfileAPI(awsclient.NewIAMClient(cfg)))
		},

		DiffFields: func(desired iaminstanceprofile.IAMInstanceProfileSpec, observed iaminstanceprofile.ObservedState, _ iaminstanceprofile.IAMInstanceProfileOutputs) []types.FieldDiff {
			return iaminstanceprofile.ComputeFieldDiffs(desired, observed)
		},
	}
}

func iamInstanceProfileLookupProbe(api iaminstanceprofile.IAMInstanceProfileAPI) LookupProbeFunc[iaminstanceprofile.IAMInstanceProfileOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (iaminstanceprofile.IAMInstanceProfileOutputs, bool, error) {
		profileName := iamInstanceProfileLookupName(filter)
		if profileName == "" {
			return iaminstanceprofile.IAMInstanceProfileOutputs{}, false, restate.TerminalError(
				fmt.Errorf("IAMInstanceProfile lookup supports id or name; tag-only lookup is not available"),
				400,
			)
		}
		observed, err := api.DescribeInstanceProfile(ctx, profileName)
		if err != nil {
			if isLookupNotFound(err, iaminstanceprofile.IsNotFound) {
				return iaminstanceprofile.IAMInstanceProfileOutputs{}, false, nil
			}
			return iaminstanceprofile.IAMInstanceProfileOutputs{}, false, err
		}
		if id := strings.TrimSpace(filter.ID); id != "" && observed.Arn != id && observed.InstanceProfileId != id && observed.InstanceProfileName != id {
			return iaminstanceprofile.IAMInstanceProfileOutputs{}, false, nil
		}
		if name := strings.TrimSpace(filter.Name); name != "" && observed.InstanceProfileName != name {
			return iaminstanceprofile.IAMInstanceProfileOutputs{}, false, nil
		}
		if !matchesLookupTags(observed.Tags, LookupFilter{Tag: filter.Tag}) {
			return iaminstanceprofile.IAMInstanceProfileOutputs{}, false, nil
		}
		return iaminstanceprofile.IAMInstanceProfileOutputs{
			Arn:                 observed.Arn,
			InstanceProfileId:   observed.InstanceProfileId,
			InstanceProfileName: observed.InstanceProfileName,
		}, true, nil
	}
}

func iamInstanceProfileLookupName(filter LookupFilter) string {
	identity := nativeLookupIdentity(filter)
	if strings.Contains(identity, ":instance-profile/") {
		return path.Base(identity)
	}
	return identity
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
	return NewGenericAdapterWithProbes(iamInstanceProfileDescriptor(), iamInstanceProfileProbe(api), iamInstanceProfileLookupProbe(api))
}
