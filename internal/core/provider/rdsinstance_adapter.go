// RDSInstance provider adapter.
//
// This file implements the provider.Adapter interface for Amazon RDS
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the RDSInstance Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + DB instance identifier.
// RDS instances are region-scoped; the key combines the AWS region and DB instance identifier.
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

// RDSInstanceAdapter implements provider.Adapter for RDSInstance (Amazon RDS) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type RDSInstanceAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI rdsinstance.RDSInstanceAPI
	apiFactory        func(aws.Config) rdsinstance.RDSInstanceAPI
}

// NewRDSInstanceAdapterWithAuth creates a production RDSInstance adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewRDSInstanceAdapterWithAuth(auth authservice.AuthClient) *RDSInstanceAdapter {
	return &RDSInstanceAdapter{auth: auth, apiFactory: func(cfg aws.Config) rdsinstance.RDSInstanceAPI {
		return rdsinstance.NewRDSInstanceAPI(awsclient.NewRDSClient(cfg))
	}}
}

// NewRDSInstanceAdapterWithAPI creates a RDSInstance adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewRDSInstanceAdapterWithAPI(api rdsinstance.RDSInstanceAPI) *RDSInstanceAdapter {
	return &RDSInstanceAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "RDSInstance" that maps template
// resource documents to this adapter in the provider registry.
func (a *RDSInstanceAdapter) Kind() string { return rdsinstance.ServiceName }

// ServiceName returns the Restate Virtual Object service name for the
// RDSInstance driver. The orchestrator uses this to dispatch durable RPCs.
func (a *RDSInstanceAdapter) ServiceName() string { return rdsinstance.ServiceName }

// Scope returns the key-scope strategy for RDSInstance resources,
// which controls how BuildKey assembles the canonical object key.
func (a *RDSInstanceAdapter) Scope() KeyScope { return KeyScopeRegion }

// BuildKey derives the canonical Restate object key for a RDSInstance resource
// from the raw JSON resource document. The key is composed of region + DB instance identifier,
// ensuring uniqueness within the Restate Virtual Object namespace.
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

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete RDSInstance spec struct expected by the driver.
func (a *RDSInstanceAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the RDSInstance Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *RDSInstanceAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[rdsinstance.RDSInstanceSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[rdsinstance.RDSInstanceSpec, rdsinstance.RDSInstanceOutputs](restate.Object[rdsinstance.RDSInstanceOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[rdsinstance.RDSInstanceOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the RDSInstance Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *RDSInstanceAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed RDSInstance driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *RDSInstanceAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[rdsinstance.RDSInstanceOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"dbIdentifier": out.DBIdentifier, "dbiResourceId": out.DbiResourceId, "arn": out.ARN, "endpoint": out.Endpoint, "port": out.Port, "engine": out.Engine, "engineVersion": out.EngineVersion, "status": out.Status}, nil
}

// Plan compares the desired RDSInstance spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *RDSInstanceAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[rdsinstance.RDSInstanceSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[rdsinstance.RDSInstanceOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("RDSInstance Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	planningAPI, err := a.planningAPI(ctx, account)
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

// BuildImportKey derives the canonical Restate object key for importing
// an existing RDSInstance resource by its region and provider-native ID.
func (a *RDSInstanceAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing RDSInstance resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
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

// decodeSpec unmarshals the raw JSON spec from a resource document into
// the typed RDSInstance spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
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

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *RDSInstanceAdapter) planningAPI(ctx restate.Context, account string) (rdsinstance.RDSInstanceAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("RDSInstance adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve RDSInstance planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
