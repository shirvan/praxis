package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/internal/drivers/auroracluster"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

type AuroraClusterAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI auroracluster.AuroraClusterAPI
	apiFactory        func(aws.Config) auroracluster.AuroraClusterAPI
}

func NewAuroraClusterAdapter() *AuroraClusterAdapter {
	return NewAuroraClusterAdapterWithRegistry(auth.LoadFromEnv())
}

func NewAuroraClusterAdapterWithRegistry(accounts *auth.Registry) *AuroraClusterAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &AuroraClusterAdapter{auth: accounts, apiFactory: func(cfg aws.Config) auroracluster.AuroraClusterAPI {
		return auroracluster.NewAuroraClusterAPI(awsclient.NewRDSClient(cfg))
	}}
}

func NewAuroraClusterAdapterWithAPI(api auroracluster.AuroraClusterAPI) *AuroraClusterAdapter {
	return &AuroraClusterAdapter{staticPlanningAPI: api}
}

func (a *AuroraClusterAdapter) Kind() string        { return auroracluster.ServiceName }
func (a *AuroraClusterAdapter) ServiceName() string { return auroracluster.ServiceName }
func (a *AuroraClusterAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *AuroraClusterAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("cluster identifier", spec.ClusterIdentifier); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.ClusterIdentifier), nil
}

func (a *AuroraClusterAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *AuroraClusterAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[auroracluster.AuroraClusterSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[auroracluster.AuroraClusterSpec, auroracluster.AuroraClusterOutputs](restate.Object[auroracluster.AuroraClusterOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[auroracluster.AuroraClusterOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *AuroraClusterAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *AuroraClusterAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[auroracluster.AuroraClusterOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"clusterIdentifier": out.ClusterIdentifier, "clusterResourceId": out.ClusterResourceId, "arn": out.ARN, "endpoint": out.Endpoint, "readerEndpoint": out.ReaderEndpoint, "port": out.Port, "engine": out.Engine, "engineVersion": out.EngineVersion, "status": out.Status}, nil
}

func (a *AuroraClusterAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[auroracluster.AuroraClusterSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[auroracluster.AuroraClusterOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("AuroraCluster Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	planningAPI, err := a.planningAPI(account)
	if err != nil {
		return "", nil, err
	}
	type describePlanResult struct {
		State auroracluster.ObservedState
		Found bool
	}
	identifier := outputs.ClusterIdentifier
	if identifier == "" {
		identifier = desired.ClusterIdentifier
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeDBCluster(runCtx, identifier)
		if descErr != nil {
			if auroracluster.IsNotFound(descErr) {
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
	rawDiffs := auroracluster.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *AuroraClusterAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *AuroraClusterAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, auroracluster.AuroraClusterOutputs](restate.Object[auroracluster.AuroraClusterOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *AuroraClusterAdapter) decodeSpec(doc resourceDocument) (auroracluster.AuroraClusterSpec, error) {
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
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return auroracluster.AuroraClusterSpec{}, fmt.Errorf("decode AuroraCluster spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return auroracluster.AuroraClusterSpec{}, fmt.Errorf("AuroraCluster metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return auroracluster.AuroraClusterSpec{}, fmt.Errorf("AuroraCluster spec.region is required")
	}
	return auroracluster.AuroraClusterSpec{Region: strings.TrimSpace(spec.Region), ClusterIdentifier: name, Engine: spec.Engine, EngineVersion: spec.EngineVersion, MasterUsername: spec.MasterUsername, MasterUserPassword: spec.MasterUserPassword, DatabaseName: spec.DatabaseName, Port: spec.Port, DBSubnetGroupName: spec.DBSubnetGroupName, DBClusterParameterGroupName: spec.DBClusterParameterGroupName, VpcSecurityGroupIds: spec.VpcSecurityGroupIds, StorageEncrypted: spec.StorageEncrypted, KMSKeyId: spec.KMSKeyId, BackupRetentionPeriod: spec.BackupRetentionPeriod, PreferredBackupWindow: spec.PreferredBackupWindow, PreferredMaintenanceWindow: spec.PreferredMaintenanceWindow, DeletionProtection: spec.DeletionProtection, EnabledCloudwatchLogsExports: spec.EnabledCloudwatchLogsExports, Tags: spec.Tags}, nil
}

func (a *AuroraClusterAdapter) planningAPI(account string) (auroracluster.AuroraClusterAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("AuroraCluster adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve AuroraCluster planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
