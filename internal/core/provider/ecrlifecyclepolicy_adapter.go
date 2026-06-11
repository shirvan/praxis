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

		PlanID: func(out ecrpolicy.ECRLifecyclePolicyOutputs) string { return out.RepositoryName },

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[ecrpolicy.ObservedState] {
			return ecrLifecyclePolicyProbe(ecrpolicy.NewLifecyclePolicyAPI(awsclient.NewECRClient(cfg)))
		},

		DiffFields: func(desired ecrpolicy.ECRLifecyclePolicySpec, observed ecrpolicy.ObservedState) []types.FieldDiff {
			rawDiffs := ecrpolicy.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// ecrLifecyclePolicyProbe adapts the driver API to the generic plan probe shape.
func ecrLifecyclePolicyProbe(api ecrpolicy.LifecyclePolicyAPI) PlanProbeFunc[ecrpolicy.ObservedState] {
	return func(runCtx restate.RunContext, repositoryName string) (ecrpolicy.ObservedState, bool, error) {
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
	return NewGenericAdapterWithProbe(ecrLifecyclePolicyDescriptor(), ecrLifecyclePolicyProbe(api))
}
