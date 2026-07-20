// ECRLifecyclePolicy provider adapter — descriptor for the GenericAdapter.
//
// Key scope: custom.
// Key parts: region + repository name.
// ECR lifecycle policies are region-scoped and tied to a repository; the key
// combines the AWS region and repository name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/ecrpolicy"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// ECRLifecyclePolicyAdapter is the descriptor-driven adapter for ECRLifecyclePolicy.
type ECRLifecyclePolicyAdapter = GenericAdapter[ecrpolicy.ECRLifecyclePolicySpec, ecrpolicy.ECRLifecyclePolicyOutputs, ecrpolicy.ObservedState]

func ecrLifecyclePolicyDescriptor() GenericDescriptor[ecrpolicy.ECRLifecyclePolicySpec, ecrpolicy.ECRLifecyclePolicyOutputs, ecrpolicy.ObservedState] {
	return GenericDescriptor[ecrpolicy.ECRLifecyclePolicySpec, ecrpolicy.ECRLifecyclePolicyOutputs, ecrpolicy.ObservedState]{
		Kind:  ecrpolicy.ServiceName,
		Scope: KeyScopeCustom,

		DecodeSpec: func(spec json.RawMessage, _ string) (ecrpolicy.ECRLifecyclePolicySpec, error) {
			var parsed ecrpolicy.ECRLifecyclePolicySpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return ecrpolicy.ECRLifecyclePolicySpec{}, fmt.Errorf("decode ECRLifecyclePolicy spec: %w", err)
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return ecrpolicy.ECRLifecyclePolicySpec{}, fmt.Errorf("ECRLifecyclePolicy spec.region is required")
			}
			if strings.TrimSpace(parsed.RepositoryName) == "" {
				return ecrpolicy.ECRLifecyclePolicySpec{}, fmt.Errorf("ECRLifecyclePolicy spec.repositoryName is required")
			}
			return parsed, nil
		},

		KeyFromSpec: func(spec ecrpolicy.ECRLifecyclePolicySpec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("repository name", spec.RepositoryName); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, spec.RepositoryName), nil
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

		PrepareSpec: func(spec ecrpolicy.ECRLifecyclePolicySpec, key, account string) ecrpolicy.ECRLifecyclePolicySpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out ecrpolicy.ECRLifecyclePolicyOutputs) map[string]any {
			result := map[string]any{"repositoryName": out.RepositoryName}
			if out.RepositoryArn != "" {
				result["repositoryArn"] = out.RepositoryArn
			}
			if out.RegistryId != "" {
				result["registryId"] = out.RegistryId
			}
			return result
		},

		PlanIdentity: storedPlanIdentity[ecrpolicy.ECRLifecyclePolicySpec](func(out ecrpolicy.ECRLifecyclePolicyOutputs) string { return out.RepositoryName }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[ecrpolicy.ECRLifecyclePolicySpec, ecrpolicy.ECRLifecyclePolicyOutputs, ecrpolicy.ObservedState] {
			return ecrLifecyclePolicyProbe(ecrpolicy.NewLifecyclePolicyAPI(awsclient.NewECRClient(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[ecrpolicy.ECRLifecyclePolicyOutputs] {
			return ecrLifecyclePolicyLookupProbe(ecrpolicy.NewLifecyclePolicyAPI(awsclient.NewECRClient(cfg)))
		},

		DiffFields: func(desired ecrpolicy.ECRLifecyclePolicySpec, observed ecrpolicy.ObservedState, _ ecrpolicy.ECRLifecyclePolicyOutputs) []types.FieldDiff {
			return ecrpolicy.ComputeFieldDiffs(desired, observed)
		},
	}
}

func ecrLifecyclePolicyLookupProbe(api ecrpolicy.LifecyclePolicyAPI) LookupProbeFunc[ecrpolicy.ECRLifecyclePolicyOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (ecrpolicy.ECRLifecyclePolicyOutputs, bool, error) {
		if len(filter.Tag) != 0 {
			return ecrpolicy.ECRLifecyclePolicyOutputs{}, false, restate.TerminalError(
				fmt.Errorf("ECRLifecyclePolicy lookup does not support tags"),
				400,
			)
		}
		identity := nativeLookupIdentity(filter)
		if identity == "" {
			return ecrpolicy.ECRLifecyclePolicyOutputs{}, false, restate.TerminalError(
				fmt.Errorf("ECRLifecyclePolicy lookup requires id or name"),
				400,
			)
		}
		observed, err := api.GetLifecyclePolicy(ctx, identity)
		if err != nil {
			if isLookupNotFound(err, ecrpolicy.IsNotFound) {
				return ecrpolicy.ECRLifecyclePolicyOutputs{}, false, nil
			}
			return ecrpolicy.ECRLifecyclePolicyOutputs{}, false, err
		}
		if !matchesNativeLookupFilter(observed.RepositoryName, nil, filter) {
			return ecrpolicy.ECRLifecyclePolicyOutputs{}, false, nil
		}
		return ecrpolicy.ECRLifecyclePolicyOutputs{
			RepositoryName: observed.RepositoryName,
			RepositoryArn:  observed.RepositoryArn,
			RegistryId:     observed.RegistryId,
		}, true, nil
	}
}

// ecrLifecyclePolicyProbe adapts the driver API to the generic plan probe shape.
func ecrLifecyclePolicyProbe(api ecrpolicy.LifecyclePolicyAPI) PlanProbeFunc[ecrpolicy.ECRLifecyclePolicySpec, ecrpolicy.ECRLifecyclePolicyOutputs, ecrpolicy.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[ecrpolicy.ECRLifecyclePolicySpec, ecrpolicy.ECRLifecyclePolicyOutputs]) (ecrpolicy.ObservedState, bool, error) {
		repositoryName := input.Identity
		obs, err := api.GetLifecyclePolicy(runCtx, repositoryName)
		if err != nil {
			if ecrpolicy.IsNotFound(err) {
				return ecrpolicy.ObservedState{}, false, nil
			}
			return ecrpolicy.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewECRLifecyclePolicyAdapterWithAuth builds the production adapter;
// plan-time credentials are resolved through the Auth Service.
func NewECRLifecyclePolicyAdapterWithAuth(auth authservice.AuthClient) *ECRLifecyclePolicyAdapter {
	return NewGenericAdapter(ecrLifecyclePolicyDescriptor(), auth)
}

// NewECRLifecyclePolicyAdapterWithAPI builds an adapter with a fixed planning
// API. Used by tests.
func NewECRLifecyclePolicyAdapterWithAPI(api ecrpolicy.LifecyclePolicyAPI) *ECRLifecyclePolicyAdapter {
	return NewGenericAdapterWithProbes(ecrLifecyclePolicyDescriptor(), ecrLifecyclePolicyProbe(api), ecrLifecyclePolicyLookupProbe(api))
}
