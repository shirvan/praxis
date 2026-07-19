// EKSCluster provider adapter — descriptor for the GenericAdapter.
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
	"github.com/shirvan/praxis/internal/drivers/ekscluster"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// EKSClusterAdapter is the descriptor-driven adapter for EKSCluster.
type EKSClusterAdapter = GenericAdapter[ekscluster.EKSClusterSpec, ekscluster.EKSClusterOutputs, ekscluster.ObservedState]

func eksClusterDescriptor() GenericDescriptor[ekscluster.EKSClusterSpec, ekscluster.EKSClusterOutputs, ekscluster.ObservedState] {
	return GenericDescriptor[ekscluster.EKSClusterSpec, ekscluster.EKSClusterOutputs, ekscluster.ObservedState]{
		Kind:  ekscluster.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (ekscluster.EKSClusterSpec, error) {
			var parsed ekscluster.EKSClusterSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return ekscluster.EKSClusterSpec{}, fmt.Errorf("decode EKSCluster spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return ekscluster.EKSClusterSpec{}, fmt.Errorf("EKSCluster metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return ekscluster.EKSClusterSpec{}, fmt.Errorf("EKSCluster spec.region is required")
			}
			if parsed.Tags == nil {
				parsed.Tags = map[string]string{}
			}
			parsed.Name = name
			return parsed, nil
		},

		KeyFromSpec: func(spec ekscluster.EKSClusterSpec, metadataName string) (string, error) {
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

		PrepareSpec: func(spec ekscluster.EKSClusterSpec, key, account string) ekscluster.EKSClusterSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out ekscluster.EKSClusterOutputs) map[string]any {
			result := map[string]any{
				"name":    out.Name,
				"status":  out.Status,
				"version": out.Version,
			}
			if out.ARN != "" {
				result["arn"] = out.ARN
			}
			if out.PlatformVersion != "" {
				result["platformVersion"] = out.PlatformVersion
			}
			if out.Endpoint != "" {
				result["endpoint"] = out.Endpoint
			}
			return result
		},

		PlanIdentity: storedPlanIdentity[ekscluster.EKSClusterSpec](func(out ekscluster.EKSClusterOutputs) string { return out.Name }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[ekscluster.EKSClusterSpec, ekscluster.EKSClusterOutputs, ekscluster.ObservedState] {
			return eksClusterProbe(ekscluster.NewEKSClusterAPI(awsclient.NewEKSClient(cfg)))
		},

		DiffFields: func(desired ekscluster.EKSClusterSpec, observed ekscluster.ObservedState, _ ekscluster.EKSClusterOutputs) []types.FieldDiff {
			return ekscluster.ComputeFieldDiffs(desired, observed)
		},
	}
}

// eksClusterProbe adapts the driver API to the generic plan probe shape.
func eksClusterProbe(api ekscluster.EKSClusterAPI) PlanProbeFunc[ekscluster.EKSClusterSpec, ekscluster.EKSClusterOutputs, ekscluster.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[ekscluster.EKSClusterSpec, ekscluster.EKSClusterOutputs]) (ekscluster.ObservedState, bool, error) {
		clusterName := input.Identity
		obs, found, err := api.DescribeCluster(runCtx, clusterName)
		if err != nil {
			if ekscluster.IsNotFound(err) {
				return ekscluster.ObservedState{}, false, nil
			}
			return ekscluster.ObservedState{}, false, err
		}
		return obs, found, nil
	}
}

// NewEKSClusterAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewEKSClusterAdapterWithAuth(auth authservice.AuthClient) *EKSClusterAdapter {
	return NewGenericAdapter(eksClusterDescriptor(), auth)
}

// NewEKSClusterAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewEKSClusterAdapterWithAPI(api ekscluster.EKSClusterAPI) *EKSClusterAdapter {
	return NewGenericAdapterWithProbe(eksClusterDescriptor(), eksClusterProbe(api))
}
