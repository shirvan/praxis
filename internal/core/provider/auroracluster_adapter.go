// AuroraCluster provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + cluster identifier.
// Aurora clusters are region-scoped; the key combines the AWS region and
// cluster identifier (the metadata.name).
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/auroracluster"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// AuroraClusterAdapter is the descriptor-driven adapter for AuroraCluster,
// extended with per-kind default timeouts and a post-provision readiness check.
type AuroraClusterAdapter struct {
	*GenericAdapter[auroracluster.AuroraClusterSpec, auroracluster.AuroraClusterOutputs, auroracluster.ObservedState]
}

func auroraClusterDescriptor() GenericDescriptor[auroracluster.AuroraClusterSpec, auroracluster.AuroraClusterOutputs, auroracluster.ObservedState] {
	return GenericDescriptor[auroracluster.AuroraClusterSpec, auroracluster.AuroraClusterOutputs, auroracluster.ObservedState]{
		Kind:  auroracluster.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (auroracluster.AuroraClusterSpec, error) {
			var spec struct {
				Region                       string            `json:"region"`
				Engine                       string            `json:"engine"`
				EngineVersion                string            `json:"engineVersion"`
				MasterUsername               string            `json:"masterUsername"`
				MasterUserPassword           string            `json:"masterUserPassword"`
				DatabaseName                 string            `json:"databaseName"`
				Port                         int32             `json:"port"`
				DBSubnetGroupName            string            `json:"dbSubnetGroupName"`
				DBClusterParameterGroupName  string            `json:"dbClusterParameterGroupName"`
				VpcSecurityGroupIds          []string          `json:"vpcSecurityGroupIds"`
				StorageEncrypted             bool              `json:"storageEncrypted"`
				KMSKeyId                     string            `json:"kmsKeyId"`
				BackupRetentionPeriod        int32             `json:"backupRetentionPeriod"`
				PreferredBackupWindow        string            `json:"preferredBackupWindow"`
				PreferredMaintenanceWindow   string            `json:"preferredMaintenanceWindow"`
				DeletionProtection           bool              `json:"deletionProtection"`
				EnabledCloudwatchLogsExports []string          `json:"enabledCloudwatchLogsExports"`
				Tags                         map[string]string `json:"tags"`
			}
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return auroracluster.AuroraClusterSpec{}, fmt.Errorf("decode AuroraCluster spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return auroracluster.AuroraClusterSpec{}, fmt.Errorf("AuroraCluster metadata.name is required")
			}
			if strings.TrimSpace(spec.Region) == "" {
				return auroracluster.AuroraClusterSpec{}, fmt.Errorf("AuroraCluster spec.region is required")
			}
			return auroracluster.AuroraClusterSpec{Region: strings.TrimSpace(spec.Region), ClusterIdentifier: name, Engine: spec.Engine, EngineVersion: spec.EngineVersion, MasterUsername: spec.MasterUsername, MasterUserPassword: spec.MasterUserPassword, DatabaseName: spec.DatabaseName, Port: spec.Port, DBSubnetGroupName: spec.DBSubnetGroupName, DBClusterParameterGroupName: spec.DBClusterParameterGroupName, VpcSecurityGroupIds: spec.VpcSecurityGroupIds, StorageEncrypted: spec.StorageEncrypted, KMSKeyId: spec.KMSKeyId, BackupRetentionPeriod: spec.BackupRetentionPeriod, PreferredBackupWindow: spec.PreferredBackupWindow, PreferredMaintenanceWindow: spec.PreferredMaintenanceWindow, DeletionProtection: spec.DeletionProtection, EnabledCloudwatchLogsExports: spec.EnabledCloudwatchLogsExports, Tags: spec.Tags}, nil
		},

		KeyFromSpec: func(spec auroracluster.AuroraClusterSpec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("cluster identifier", spec.ClusterIdentifier); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, spec.ClusterIdentifier), nil
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

		PrepareSpec: func(spec auroracluster.AuroraClusterSpec, _ string, account string) auroracluster.AuroraClusterSpec {
			spec.Account = account
			return spec
		},

		NormalizeOutputs: func(out auroracluster.AuroraClusterOutputs) map[string]any {
			return map[string]any{"clusterIdentifier": out.ClusterIdentifier, "clusterResourceId": out.ClusterResourceId, "arn": out.ARN, "endpoint": out.Endpoint, "readerEndpoint": out.ReaderEndpoint, "port": out.Port, "engine": out.Engine, "engineVersion": out.EngineVersion, "status": out.Status}
		},

		PlanIdentity: storedPlanIdentity[auroracluster.AuroraClusterSpec](func(out auroracluster.AuroraClusterOutputs) string { return out.ClusterIdentifier }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[auroracluster.AuroraClusterSpec, auroracluster.AuroraClusterOutputs, auroracluster.ObservedState] {
			return auroraClusterProbe(auroracluster.NewAuroraClusterAPI(awsclient.NewRDSClient(cfg)))
		},

		DiffFields: func(desired auroracluster.AuroraClusterSpec, observed auroracluster.ObservedState, _ auroracluster.AuroraClusterOutputs) []types.FieldDiff {
			rawDiffs := auroracluster.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
		SensitiveFields: []string{"spec.masterUserPassword"},
	}
}

// auroraClusterProbe adapts the driver API to the generic plan probe shape.
func auroraClusterProbe(api auroracluster.AuroraClusterAPI) PlanProbeFunc[auroracluster.AuroraClusterSpec, auroracluster.AuroraClusterOutputs, auroracluster.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[auroracluster.AuroraClusterSpec, auroracluster.AuroraClusterOutputs]) (auroracluster.ObservedState, bool, error) {
		identifier := input.Identity
		obs, err := api.DescribeDBCluster(runCtx, identifier)
		if err != nil {
			if auroracluster.IsNotFound(err) {
				return auroracluster.ObservedState{}, false, nil
			}
			return auroracluster.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewAuroraClusterAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewAuroraClusterAdapterWithAuth(auth authservice.AuthClient) *AuroraClusterAdapter {
	return &AuroraClusterAdapter{GenericAdapter: NewGenericAdapter(auroraClusterDescriptor(), auth)}
}

// NewAuroraClusterAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewAuroraClusterAdapterWithAPI(api auroracluster.AuroraClusterAPI) *AuroraClusterAdapter {
	return &AuroraClusterAdapter{GenericAdapter: NewGenericAdapterWithProbe(auroraClusterDescriptor(), auroraClusterProbe(api))}
}

// DefaultTimeouts provides per-kind default timeouts for Aurora clusters.
func (a *AuroraClusterAdapter) DefaultTimeouts() types.ResourceTimeouts {
	return types.ResourceTimeouts{Create: "30m", Update: "30m", Delete: "30m"}
}

// WaitReady checks whether the Aurora cluster has reached the available state.
func (a *AuroraClusterAdapter) WaitReady(ctx restate.Context, key string) (WaitReadyResult, error) {
	status, err := restate.Object[types.StatusResponse](ctx, a.ServiceName(), key, "GetStatus").Request(restate.Void{})
	if err != nil {
		return WaitReadyResult{}, err
	}
	if status.Status == types.StatusReady {
		outputs, _ := fetchJSONMap(ctx, a.ServiceName(), key, "GetOutputs")
		return WaitReadyResult{Ready: true, Message: "cluster available", Outputs: outputs}, nil
	}
	return WaitReadyResult{Ready: false, Message: fmt.Sprintf("cluster status: %s", status.Status)}, nil
}
