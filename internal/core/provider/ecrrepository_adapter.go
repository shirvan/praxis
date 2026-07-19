// ECRRepository provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + repository name.
// ECR repositories are region-scoped; the key combines the AWS region and
// repository name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/ecrrepo"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// ECRRepositoryAdapter is the descriptor-driven adapter for ECRRepository.
type ECRRepositoryAdapter struct {
	*GenericAdapter[ecrrepo.ECRRepositorySpec, ecrrepo.ECRRepositoryOutputs, ecrrepo.ObservedState]
}

func ecrRepositoryDescriptor() GenericDescriptor[ecrrepo.ECRRepositorySpec, ecrrepo.ECRRepositoryOutputs, ecrrepo.ObservedState] {
	return GenericDescriptor[ecrrepo.ECRRepositorySpec, ecrrepo.ECRRepositoryOutputs, ecrrepo.ObservedState]{
		Kind:  ecrrepo.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (ecrrepo.ECRRepositorySpec, error) {
			var parsed ecrrepo.ECRRepositorySpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return ecrrepo.ECRRepositorySpec{}, fmt.Errorf("decode ECRRepository spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return ecrrepo.ECRRepositorySpec{}, fmt.Errorf("ECRRepository metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return ecrrepo.ECRRepositorySpec{}, fmt.Errorf("ECRRepository spec.region is required")
			}
			parsed.RepositoryName = name
			if parsed.Tags == nil {
				parsed.Tags = map[string]string{}
			}
			return parsed, nil
		},

		KeyFromSpec: func(spec ecrrepo.ECRRepositorySpec, _ string) (string, error) {
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

		PrepareSpec: func(spec ecrrepo.ECRRepositorySpec, key, account string) ecrrepo.ECRRepositorySpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out ecrrepo.ECRRepositoryOutputs) map[string]any {
			return map[string]any{
				"repositoryArn":  out.RepositoryArn,
				"repositoryName": out.RepositoryName,
				"repositoryUri":  out.RepositoryUri,
				"registryId":     out.RegistryId,
			}
		},

		PlanIdentity: storedPlanIdentity[ecrrepo.ECRRepositorySpec](func(out ecrrepo.ECRRepositoryOutputs) string { return out.RepositoryName }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[ecrrepo.ECRRepositorySpec, ecrrepo.ECRRepositoryOutputs, ecrrepo.ObservedState] {
			return ecrRepositoryProbe(ecrrepo.NewRepositoryAPI(awsclient.NewECRClient(cfg)))
		},

		DiffFields: func(desired ecrrepo.ECRRepositorySpec, observed ecrrepo.ObservedState, _ ecrrepo.ECRRepositoryOutputs) []types.FieldDiff {
			return ecrrepo.ComputeFieldDiffs(desired, observed)
		},
	}
}

// ecrRepositoryProbe adapts the driver API to the generic plan probe shape.
func ecrRepositoryProbe(api ecrrepo.RepositoryAPI) PlanProbeFunc[ecrrepo.ECRRepositorySpec, ecrrepo.ECRRepositoryOutputs, ecrrepo.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[ecrrepo.ECRRepositorySpec, ecrrepo.ECRRepositoryOutputs]) (ecrrepo.ObservedState, bool, error) {
		repositoryName := input.Identity
		obs, err := api.DescribeRepository(runCtx, repositoryName)
		if err != nil {
			if ecrrepo.IsNotFound(err) {
				return ecrrepo.ObservedState{}, false, nil
			}
			return ecrrepo.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewECRRepositoryAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewECRRepositoryAdapterWithAuth(auth authservice.AuthClient) *ECRRepositoryAdapter {
	return &ECRRepositoryAdapter{GenericAdapter: NewGenericAdapter(ecrRepositoryDescriptor(), auth)}
}

// NewECRRepositoryAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewECRRepositoryAdapterWithAPI(api ecrrepo.RepositoryAPI) *ECRRepositoryAdapter {
	return &ECRRepositoryAdapter{GenericAdapter: NewGenericAdapterWithProbe(ecrRepositoryDescriptor(), ecrRepositoryProbe(api))}
}
