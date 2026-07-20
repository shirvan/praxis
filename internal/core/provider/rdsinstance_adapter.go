// RDSInstance provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + DB instance identifier.
// RDS instances are region-scoped; the key combines the AWS region and DB
// instance identifier (the metadata.name).
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/rdsinstance"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// RDSInstanceAdapter is the descriptor-driven adapter for RDSInstance,
// extended with per-kind default timeouts and a post-provision readiness check.
type RDSInstanceAdapter struct {
	*GenericAdapter[rdsinstance.RDSInstanceSpec, rdsinstance.RDSInstanceOutputs, rdsinstance.ObservedState]
}

func rdsInstanceDescriptor() GenericDescriptor[rdsinstance.RDSInstanceSpec, rdsinstance.RDSInstanceOutputs, rdsinstance.ObservedState] {
	return GenericDescriptor[rdsinstance.RDSInstanceSpec, rdsinstance.RDSInstanceOutputs, rdsinstance.ObservedState]{
		Kind:  rdsinstance.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (rdsinstance.RDSInstanceSpec, error) {
			var spec struct {
				Region                     string            `json:"region"`
				Engine                     string            `json:"engine"`
				EngineVersion              string            `json:"engineVersion"`
				InstanceClass              string            `json:"instanceClass"`
				AllocatedStorage           int32             `json:"allocatedStorage"`
				StorageType                string            `json:"storageType"`
				IOPS                       int32             `json:"iops"`
				StorageThroughput          int32             `json:"storageThroughput"`
				StorageEncrypted           bool              `json:"storageEncrypted"`
				KMSKeyId                   string            `json:"kmsKeyId"`
				MasterUsername             string            `json:"masterUsername"`
				MasterUserPassword         string            `json:"masterUserPassword"`
				DBSubnetGroupName          string            `json:"dbSubnetGroupName"`
				ParameterGroupName         string            `json:"parameterGroupName"`
				VpcSecurityGroupIds        []string          `json:"vpcSecurityGroupIds"`
				DBClusterIdentifier        string            `json:"dbClusterIdentifier"`
				MultiAZ                    bool              `json:"multiAZ"`
				PubliclyAccessible         bool              `json:"publiclyAccessible"`
				BackupRetentionPeriod      int32             `json:"backupRetentionPeriod"`
				PreferredBackupWindow      string            `json:"preferredBackupWindow"`
				PreferredMaintenanceWindow string            `json:"preferredMaintenanceWindow"`
				DeletionProtection         bool              `json:"deletionProtection"`
				AutoMinorVersionUpgrade    bool              `json:"autoMinorVersionUpgrade"`
				MonitoringInterval         int32             `json:"monitoringInterval"`
				MonitoringRoleArn          string            `json:"monitoringRoleArn"`
				PerformanceInsightsEnabled bool              `json:"performanceInsightsEnabled"`
				Tags                       map[string]string `json:"tags"`
			}
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return rdsinstance.RDSInstanceSpec{}, fmt.Errorf("decode RDSInstance spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return rdsinstance.RDSInstanceSpec{}, fmt.Errorf("RDSInstance metadata.name is required")
			}
			if strings.TrimSpace(spec.Region) == "" {
				return rdsinstance.RDSInstanceSpec{}, fmt.Errorf("RDSInstance spec.region is required")
			}
			return rdsinstance.RDSInstanceSpec{Region: strings.TrimSpace(spec.Region), DBIdentifier: name, Engine: spec.Engine, EngineVersion: spec.EngineVersion, InstanceClass: spec.InstanceClass, AllocatedStorage: spec.AllocatedStorage, StorageType: spec.StorageType, IOPS: spec.IOPS, StorageThroughput: spec.StorageThroughput, StorageEncrypted: spec.StorageEncrypted, KMSKeyId: spec.KMSKeyId, MasterUsername: spec.MasterUsername, MasterUserPassword: spec.MasterUserPassword, DBSubnetGroupName: spec.DBSubnetGroupName, ParameterGroupName: spec.ParameterGroupName, VpcSecurityGroupIds: spec.VpcSecurityGroupIds, DBClusterIdentifier: spec.DBClusterIdentifier, MultiAZ: spec.MultiAZ, PubliclyAccessible: spec.PubliclyAccessible, BackupRetentionPeriod: spec.BackupRetentionPeriod, PreferredBackupWindow: spec.PreferredBackupWindow, PreferredMaintenanceWindow: spec.PreferredMaintenanceWindow, DeletionProtection: spec.DeletionProtection, AutoMinorVersionUpgrade: spec.AutoMinorVersionUpgrade, MonitoringInterval: spec.MonitoringInterval, MonitoringRoleArn: spec.MonitoringRoleArn, PerformanceInsightsEnabled: spec.PerformanceInsightsEnabled, Tags: spec.Tags}, nil
		},

		KeyFromSpec: func(spec rdsinstance.RDSInstanceSpec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("db identifier", spec.DBIdentifier); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, spec.DBIdentifier), nil
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

		PrepareSpec: func(spec rdsinstance.RDSInstanceSpec, _ string, account string) rdsinstance.RDSInstanceSpec {
			spec.Account = account
			return spec
		},

		NormalizeOutputs: func(out rdsinstance.RDSInstanceOutputs) map[string]any {
			return map[string]any{"dbIdentifier": out.DBIdentifier, "dbiResourceId": out.DbiResourceId, "arn": out.ARN, "endpoint": out.Endpoint, "port": out.Port, "engine": out.Engine, "engineVersion": out.EngineVersion, "status": out.Status}
		},

		PlanIdentity: storedPlanIdentity[rdsinstance.RDSInstanceSpec](func(out rdsinstance.RDSInstanceOutputs) string { return out.DBIdentifier }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[rdsinstance.RDSInstanceSpec, rdsinstance.RDSInstanceOutputs, rdsinstance.ObservedState] {
			return rdsInstanceProbe(rdsinstance.NewRDSInstanceAPI(awsclient.NewRDSClient(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[rdsinstance.RDSInstanceOutputs] {
			return rdsInstanceLookupProbe(rdsinstance.NewRDSInstanceAPI(awsclient.NewRDSClient(cfg)))
		},

		DiffFields: func(desired rdsinstance.RDSInstanceSpec, observed rdsinstance.ObservedState, _ rdsinstance.RDSInstanceOutputs) []types.FieldDiff {
			return rdsinstance.ComputeFieldDiffs(desired, observed)
		},
		SensitiveFields: []string{"spec.masterUserPassword"},
	}
}

// rdsInstanceProbe adapts the driver API to the generic plan probe shape.
func rdsInstanceProbe(api rdsinstance.RDSInstanceAPI) PlanProbeFunc[rdsinstance.RDSInstanceSpec, rdsinstance.RDSInstanceOutputs, rdsinstance.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[rdsinstance.RDSInstanceSpec, rdsinstance.RDSInstanceOutputs]) (rdsinstance.ObservedState, bool, error) {
		identifier := input.Identity
		obs, err := api.DescribeDBInstance(runCtx, identifier)
		if err != nil {
			if rdsinstance.IsNotFound(err) {
				return rdsinstance.ObservedState{}, false, nil
			}
			return rdsinstance.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

func rdsInstanceLookupProbe(api rdsinstance.RDSInstanceAPI) LookupProbeFunc[rdsinstance.RDSInstanceOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (rdsinstance.RDSInstanceOutputs, bool, error) {
		identity := nativeLookupIdentity(filter)
		if identity == "" {
			return rdsinstance.RDSInstanceOutputs{}, false, restate.TerminalError(
				fmt.Errorf("RDSInstance lookup supports id or name; tag-only lookup is not available"),
				400,
			)
		}
		observed, err := api.DescribeDBInstance(ctx, identity)
		if err != nil {
			if isLookupNotFound(err, rdsinstance.IsNotFound) {
				return rdsinstance.RDSInstanceOutputs{}, false, nil
			}
			return rdsinstance.RDSInstanceOutputs{}, false, err
		}
		if !matchesNativeLookupFilter(observed.DBIdentifier, observed.Tags, filter) {
			return rdsinstance.RDSInstanceOutputs{}, false, nil
		}
		return rdsinstance.RDSInstanceOutputs{
			DBIdentifier:  observed.DBIdentifier,
			DbiResourceId: observed.DbiResourceId,
			ARN:           observed.ARN,
			Endpoint:      observed.Endpoint,
			Port:          observed.Port,
			Engine:        observed.Engine,
			EngineVersion: observed.EngineVersion,
			Status:        observed.Status,
		}, true, nil
	}
}

// NewRDSInstanceAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewRDSInstanceAdapterWithAuth(auth authservice.AuthClient) *RDSInstanceAdapter {
	return &RDSInstanceAdapter{GenericAdapter: NewGenericAdapter(rdsInstanceDescriptor(), auth)}
}

// NewRDSInstanceAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewRDSInstanceAdapterWithAPI(api rdsinstance.RDSInstanceAPI) *RDSInstanceAdapter {
	return &RDSInstanceAdapter{GenericAdapter: NewGenericAdapterWithProbes(rdsInstanceDescriptor(), rdsInstanceProbe(api), rdsInstanceLookupProbe(api))}
}

// DefaultTimeouts provides per-kind default timeouts for RDS instances.
func (a *RDSInstanceAdapter) DefaultTimeouts() types.ResourceTimeouts {
	return types.ResourceTimeouts{Create: "30m", Update: "30m", Delete: "30m"}
}

// WaitReady checks whether the RDS instance has reached the available state.
func (a *RDSInstanceAdapter) WaitReady(ctx restate.Context, key string) (WaitReadyResult, error) {
	status, err := restate.Object[types.StatusResponse](ctx, a.ServiceName(), key, "GetStatus").Request(restate.Void{})
	if err != nil {
		return WaitReadyResult{}, err
	}
	if status.Status == types.StatusReady {
		outputs, _ := fetchJSONMap(ctx, a.ServiceName(), key, "GetOutputs")
		return WaitReadyResult{Ready: true, Message: "instance available", Outputs: outputs}, nil
	}
	return WaitReadyResult{Ready: false, Message: fmt.Sprintf("instance status: %s", status.Status)}, nil
}
