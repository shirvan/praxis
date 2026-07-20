// IAMRole provider adapter — descriptor for the GenericAdapter.
//
// Key scope: global (IAM is region-free).
// Key parts: role name (optionally with path prefix).
// IAM roles are global; the key is derived from the role name.
package provider

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/iamrole"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type IAMRoleAdapter struct {
	*GenericAdapter[iamrole.IAMRoleSpec, iamrole.IAMRoleOutputs, iamrole.ObservedState]
}

func iamRoleDescriptor() GenericDescriptor[iamrole.IAMRoleSpec, iamrole.IAMRoleOutputs, iamrole.ObservedState] {
	return GenericDescriptor[iamrole.IAMRoleSpec, iamrole.IAMRoleOutputs, iamrole.ObservedState]{
		Kind:  iamrole.ServiceName,
		Scope: KeyScopeGlobal,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (iamrole.IAMRoleSpec, error) {
			var spec struct {
				Path                     string            `json:"path"`
				AssumeRolePolicyDocument string            `json:"assumeRolePolicyDocument"`
				Description              string            `json:"description"`
				MaxSessionDuration       int32             `json:"maxSessionDuration"`
				PermissionsBoundary      string            `json:"permissionsBoundary"`
				InlinePolicies           map[string]string `json:"inlinePolicies"`
				ManagedPolicyArns        []string          `json:"managedPolicyArns"`
				Tags                     map[string]string `json:"tags"`
			}
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return iamrole.IAMRoleSpec{}, fmt.Errorf("decode IAMRole spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return iamrole.IAMRoleSpec{}, fmt.Errorf("IAMRole metadata.name is required")
			}
			if strings.TrimSpace(spec.AssumeRolePolicyDocument) == "" {
				return iamrole.IAMRoleSpec{}, fmt.Errorf("IAMRole spec.assumeRolePolicyDocument is required")
			}
			if spec.Path == "" {
				spec.Path = "/"
			}
			if spec.MaxSessionDuration == 0 {
				spec.MaxSessionDuration = 3600
			}
			if spec.InlinePolicies == nil {
				spec.InlinePolicies = map[string]string{}
			}
			if spec.ManagedPolicyArns == nil {
				spec.ManagedPolicyArns = []string{}
			}
			if spec.Tags == nil {
				spec.Tags = map[string]string{}
			}
			return iamrole.IAMRoleSpec{
				Path:                     spec.Path,
				RoleName:                 name,
				AssumeRolePolicyDocument: spec.AssumeRolePolicyDocument,
				Description:              spec.Description,
				MaxSessionDuration:       spec.MaxSessionDuration,
				PermissionsBoundary:      spec.PermissionsBoundary,
				InlinePolicies:           spec.InlinePolicies,
				ManagedPolicyArns:        spec.ManagedPolicyArns,
				Tags:                     spec.Tags,
			}, nil
		},

		KeyFromSpec: func(spec iamrole.IAMRoleSpec, _ string) (string, error) {
			if err := ValidateKeyPart("role name", spec.RoleName); err != nil {
				return "", err
			}
			return spec.RoleName, nil
		},

		ImportKey: func(_, resourceID string) (string, error) {
			if err := ValidateKeyPart("resource ID", resourceID); err != nil {
				return "", err
			}
			return resourceID, nil
		},

		PrepareSpec: func(spec iamrole.IAMRoleSpec, _, account string) iamrole.IAMRoleSpec {
			spec.Account = account
			return spec
		},

		NormalizeOutputs: func(out iamrole.IAMRoleOutputs) map[string]any {
			return map[string]any{"arn": out.Arn, "roleId": out.RoleId, "roleName": out.RoleName}
		},

		PlanIdentity: storedPlanIdentity[iamrole.IAMRoleSpec](func(out iamrole.IAMRoleOutputs) string { return out.RoleName }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[iamrole.IAMRoleSpec, iamrole.IAMRoleOutputs, iamrole.ObservedState] {
			return iamRoleProbe(iamrole.NewIAMRoleAPI(awsclient.NewIAMClient(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[iamrole.IAMRoleOutputs] {
			return iamRoleLookupProbe(iamrole.NewIAMRoleAPI(awsclient.NewIAMClient(cfg)))
		},

		DiffFields: func(desired iamrole.IAMRoleSpec, observed iamrole.ObservedState, _ iamrole.IAMRoleOutputs) []types.FieldDiff {
			return iamrole.ComputeFieldDiffs(desired, observed)
		},
	}
}

// iamRoleProbe adapts the driver API to the generic plan probe shape.
func iamRoleProbe(api iamrole.IAMRoleAPI) PlanProbeFunc[iamrole.IAMRoleSpec, iamrole.IAMRoleOutputs, iamrole.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[iamrole.IAMRoleSpec, iamrole.IAMRoleOutputs]) (iamrole.ObservedState, bool, error) {
		roleName := input.Identity
		obs, err := api.DescribeRole(runCtx, roleName)
		if err != nil {
			if iamrole.IsNotFound(err) {
				return iamrole.ObservedState{}, false, nil
			}
			return iamrole.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

func iamRoleLookupProbe(api iamrole.IAMRoleAPI) LookupProbeFunc[iamrole.IAMRoleOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (iamrole.IAMRoleOutputs, bool, error) {
		observed, err := lookupIAMRole(ctx, api, filter)
		if err != nil {
			if isLookupNotFound(err, iamrole.IsNotFound) {
				return iamrole.IAMRoleOutputs{}, false, nil
			}
			return iamrole.IAMRoleOutputs{}, false, err
		}
		if !matchesIAMRoleFilter(observed, filter) {
			return iamrole.IAMRoleOutputs{}, false, nil
		}
		return iamrole.IAMRoleOutputs{
			Arn:      observed.Arn,
			RoleId:   observed.RoleId,
			RoleName: observed.RoleName,
		}, true, nil
	}
}

// NewIAMRoleAdapterWithAuth builds the production adapter; plan- and
// lookup-time credentials are resolved through the Auth Service.
func NewIAMRoleAdapterWithAuth(auth authservice.AuthClient) *IAMRoleAdapter {
	return &IAMRoleAdapter{
		GenericAdapter: NewGenericAdapter(iamRoleDescriptor(), auth),
	}
}

// NewIAMRoleAdapterWithAPI builds an adapter with a fixed planning API used
// for both Plan probes and Lookup. Used by tests.
func NewIAMRoleAdapterWithAPI(api iamrole.IAMRoleAPI) *IAMRoleAdapter {
	return &IAMRoleAdapter{
		GenericAdapter: NewGenericAdapterWithProbes(iamRoleDescriptor(), iamRoleProbe(api), iamRoleLookupProbe(api)),
	}
}

func lookupIAMRole(ctx restate.RunContext, api iamrole.IAMRoleAPI, filter LookupFilter) (iamrole.ObservedState, error) {
	roleName := normalizeIAMRoleLookupName(filter)
	if roleName == "" && len(filter.Tag) > 0 {
		resolved, err := api.FindByTags(ctx, filter.Tag)
		if err != nil {
			return iamrole.ObservedState{}, err
		}
		roleName = strings.TrimSpace(resolved)
	}
	if roleName == "" {
		return iamrole.ObservedState{}, fmt.Errorf("not found")
	}
	return api.DescribeRole(ctx, roleName)
}

func normalizeIAMRoleLookupName(filter LookupFilter) string {
	value := strings.TrimSpace(filter.ID)
	if value == "" {
		value = strings.TrimSpace(filter.Name)
	}
	if value == "" {
		return ""
	}
	if strings.Contains(value, ":role/") {
		return path.Base(value)
	}
	return value
}

func matchesIAMRoleFilter(observed iamrole.ObservedState, filter LookupFilter) bool {
	lookupName := normalizeIAMRoleLookupName(filter)
	if lookupName != "" && observed.RoleName != lookupName {
		return false
	}
	for key, value := range filter.Tag {
		if observed.Tags[key] != value {
			return false
		}
	}
	return true
}
