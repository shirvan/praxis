package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/drivers/rdsinstance"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type RDSInstanceAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI rdsinstance.RDSInstanceAPI
	apiFactory        func(aws.Config) rdsinstance.RDSInstanceAPI
}

func NewRDSInstanceAdapter() *RDSInstanceAdapter {
	return NewRDSInstanceAdapterWithRegistry(auth.LoadFromEnv())
}

func NewRDSInstanceAdapterWithRegistry(accounts *auth.Registry) *RDSInstanceAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &RDSInstanceAdapter{auth: accounts, apiFactory: func(cfg aws.Config) rdsinstance.RDSInstanceAPI {
		return rdsinstance.NewRDSInstanceAPI(awsclient.NewRDSClient(cfg))
	}}
}

func NewRDSInstanceAdapterWithAPI(api rdsinstance.RDSInstanceAPI) *RDSInstanceAdapter {
	return &RDSInstanceAdapter{staticPlanningAPI: api}
}

func (a *RDSInstanceAdapter) Kind() string        { return rdsinstance.ServiceName }
func (a *RDSInstanceAdapter) ServiceName() string { return rdsinstance.ServiceName }
func (a *RDSInstanceAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *RDSInstanceAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("region", spec.Region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("db identifier", spec.DBIdentifier); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.DBIdentifier), nil
}

func (a *RDSInstanceAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *RDSInstanceAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[rdsinstance.RDSInstanceSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[rdsinstance.RDSInstanceSpec, rdsinstance.RDSInstanceOutputs](restate.Object[rdsinstance.RDSInstanceOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[rdsinstance.RDSInstanceOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *RDSInstanceAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *RDSInstanceAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[rdsinstance.RDSInstanceOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"dbIdentifier": out.DBIdentifier, "dbiResourceId": out.DbiResourceId, "arn": out.ARN, "endpoint": out.Endpoint, "port": out.Port, "engine": out.Engine, "engineVersion": out.EngineVersion, "status": out.Status}, nil
}

func (a *RDSInstanceAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[rdsinstance.RDSInstanceSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[rdsinstance.RDSInstanceOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("RDSInstance Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	planningAPI, err := a.planningAPI(account)
	if err != nil {
		return "", nil, err
	}
	type describePlanResult struct {
		State rdsinstance.ObservedState
		Found bool
	}
	identifier := outputs.DBIdentifier
	if identifier == "" {
		identifier = desired.DBIdentifier
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeDBInstance(runCtx, identifier)
		if descErr != nil {
			if rdsinstance.IsNotFound(descErr) {
				return describePlanResult{Found: false}, nil
			}
			return describePlanResult{}, restate.TerminalError(descErr, 500)
		}
		return describePlanResult{State: obs, Found: true}, nil
	})
	if err != nil {
		return "", nil, err
	}
	if !result.Found {
		fields, fieldErr := createFieldDiffsFromSpec(desired)
		if fieldErr != nil {
			return "", nil, fieldErr
		}
		return types.OpCreate, fields, nil
	}
	rawDiffs := rdsinstance.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *RDSInstanceAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *RDSInstanceAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, rdsinstance.RDSInstanceOutputs](restate.Object[rdsinstance.RDSInstanceOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *RDSInstanceAdapter) decodeSpec(doc resourceDocument) (rdsinstance.RDSInstanceSpec, error) {
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
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return rdsinstance.RDSInstanceSpec{}, fmt.Errorf("decode RDSInstance spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return rdsinstance.RDSInstanceSpec{}, fmt.Errorf("RDSInstance metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return rdsinstance.RDSInstanceSpec{}, fmt.Errorf("RDSInstance spec.region is required")
	}
	return rdsinstance.RDSInstanceSpec{Region: strings.TrimSpace(spec.Region), DBIdentifier: name, Engine: spec.Engine, EngineVersion: spec.EngineVersion, InstanceClass: spec.InstanceClass, AllocatedStorage: spec.AllocatedStorage, StorageType: spec.StorageType, IOPS: spec.IOPS, StorageThroughput: spec.StorageThroughput, StorageEncrypted: spec.StorageEncrypted, KMSKeyId: spec.KMSKeyId, MasterUsername: spec.MasterUsername, MasterUserPassword: spec.MasterUserPassword, DBSubnetGroupName: spec.DBSubnetGroupName, ParameterGroupName: spec.ParameterGroupName, VpcSecurityGroupIds: spec.VpcSecurityGroupIds, DBClusterIdentifier: spec.DBClusterIdentifier, MultiAZ: spec.MultiAZ, PubliclyAccessible: spec.PubliclyAccessible, BackupRetentionPeriod: spec.BackupRetentionPeriod, PreferredBackupWindow: spec.PreferredBackupWindow, PreferredMaintenanceWindow: spec.PreferredMaintenanceWindow, DeletionProtection: spec.DeletionProtection, AutoMinorVersionUpgrade: spec.AutoMinorVersionUpgrade, MonitoringInterval: spec.MonitoringInterval, MonitoringRoleArn: spec.MonitoringRoleArn, PerformanceInsightsEnabled: spec.PerformanceInsightsEnabled, Tags: spec.Tags}, nil
}

func (a *RDSInstanceAdapter) planningAPI(account string) (rdsinstance.RDSInstanceAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("RDSInstance adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve RDSInstance planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}