// ECSCluster provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + cluster name.
// Cluster names are unique within a region; the key combines the AWS region and
// the cluster name (metadata.name).
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/ecscluster"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// ECSClusterAdapter is the descriptor-driven adapter for ECSCluster.
type ECSClusterAdapter = GenericAdapter[ecscluster.ECSClusterSpec, ecscluster.ECSClusterOutputs, ecscluster.ObservedState]

func ecsClusterDescriptor() GenericDescriptor[ecscluster.ECSClusterSpec, ecscluster.ECSClusterOutputs, ecscluster.ObservedState] {
	return GenericDescriptor[ecscluster.ECSClusterSpec, ecscluster.ECSClusterOutputs, ecscluster.ObservedState]{
		Kind:  ecscluster.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (ecscluster.ECSClusterSpec, error) {
			var parsed ecscluster.ECSClusterSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return ecscluster.ECSClusterSpec{}, fmt.Errorf("decode ECSCluster spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return ecscluster.ECSClusterSpec{}, fmt.Errorf("ECSCluster metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return ecscluster.ECSClusterSpec{}, fmt.Errorf("ECSCluster spec.region is required")
			}
			if parsed.Tags == nil {
				parsed.Tags = map[string]string{}
			}
			parsed.Name = name
			return parsed, nil
		},

		KeyFromSpec: func(spec ecscluster.ECSClusterSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("cluster name", name); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, name), nil
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

		PrepareSpec: func(spec ecscluster.ECSClusterSpec, key, account string) ecscluster.ECSClusterSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out ecscluster.ECSClusterOutputs) map[string]any {
			result := map[string]any{
				"name":   out.Name,
				"status": out.Status,
			}
			if out.ARN != "" {
				result["arn"] = out.ARN
			}
			return result
		},

		PlanIdentity: storedPlanIdentity[ecscluster.ECSClusterSpec](func(out ecscluster.ECSClusterOutputs) string { return out.Name }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[ecscluster.ECSClusterSpec, ecscluster.ECSClusterOutputs, ecscluster.ObservedState] {
			return ecsClusterProbe(ecscluster.NewECSClusterAPI(awsclient.NewECSClient(cfg)))
		},

		DiffFields: func(desired ecscluster.ECSClusterSpec, observed ecscluster.ObservedState, _ ecscluster.ECSClusterOutputs) []types.FieldDiff {
			return ecscluster.ComputeFieldDiffs(desired, observed)
		},
	}
}

// ecsClusterProbe adapts the driver API to the generic plan probe shape.
func ecsClusterProbe(api ecscluster.ECSClusterAPI) PlanProbeFunc[ecscluster.ECSClusterSpec, ecscluster.ECSClusterOutputs, ecscluster.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[ecscluster.ECSClusterSpec, ecscluster.ECSClusterOutputs]) (ecscluster.ObservedState, bool, error) {
		clusterName := input.Identity
		obs, found, err := api.DescribeCluster(runCtx, clusterName)
		if err != nil {
			if ecscluster.IsNotFound(err) {
				return ecscluster.ObservedState{}, false, nil
			}
			return ecscluster.ObservedState{}, false, err
		}
		return obs, found, nil
	}
}

// NewECSClusterAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewECSClusterAdapterWithAuth(auth authservice.AuthClient) *ECSClusterAdapter {
	return NewGenericAdapter(ecsClusterDescriptor(), auth)
}

// NewECSClusterAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewECSClusterAdapterWithAPI(api ecscluster.ECSClusterAPI) *ECSClusterAdapter {
	return NewGenericAdapterWithProbe(ecsClusterDescriptor(), ecsClusterProbe(api))
}
