// AuroraCluster provider adapter.
//
// This file implements the provider.Adapter interface for Amazon Aurora
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the AuroraCluster Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + cluster identifier.
// Aurora clusters are region-scoped; the key combines the AWS region and cluster identifier.
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

// AuroraClusterAdapter implements provider.Adapter for AuroraCluster (Amazon Aurora) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type AuroraClusterAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI auroracluster.AuroraClusterAPI
	apiFactory        func(aws.Config) auroracluster.AuroraClusterAPI
}

// NewAuroraClusterAdapterWithAuth creates a production AuroraCluster adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewAuroraClusterAdapterWithAuth(auth authservice.AuthClient) *AuroraClusterAdapter {
	return &AuroraClusterAdapter{auth: auth, apiFactory: func(cfg aws.Config) auroracluster.AuroraClusterAPI {
		return auroracluster.NewAuroraClusterAPI(awsclient.NewRDSClient(cfg))
	}}
}

// NewAuroraClusterAdapterWithAPI creates a AuroraCluster adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewAuroraClusterAdapterWithAPI(api auroracluster.AuroraClusterAPI) *AuroraClusterAdapter {
	return &AuroraClusterAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "AuroraCluster" that maps template
// resource documents to this adapter in the provider registry.
func (a *AuroraClusterAdapter) Kind() string        { return auroracluster.ServiceName }
// ServiceName returns the Restate Virtual Object service name for the
// AuroraCluster driver. The orchestrator uses this to dispatch durable RPCs.
func (a *AuroraClusterAdapter) ServiceName() string { return auroracluster.ServiceName }
// Scope returns the key-scope strategy for AuroraCluster resources,
// which controls how BuildKey assembles the canonical object key.
func (a *AuroraClusterAdapter) Scope() KeyScope     { return KeyScopeRegion }

// BuildKey derives the canonical Restate object key for a AuroraCluster resource
// from the raw JSON resource document. The key is composed of region + cluster identifier,
// ensuring uniqueness within the Restate Virtual Object namespace.
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

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete AuroraCluster spec struct expected by the driver.
func (a *AuroraClusterAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the AuroraCluster Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *AuroraClusterAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[auroracluster.AuroraClusterSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[auroracluster.AuroraClusterSpec, auroracluster.AuroraClusterOutputs](restate.Object[auroracluster.AuroraClusterOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[auroracluster.AuroraClusterOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the AuroraCluster Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *AuroraClusterAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed AuroraCluster driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *AuroraClusterAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[auroracluster.AuroraClusterOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"clusterIdentifier": out.ClusterIdentifier, "clusterResourceId": out.ClusterResourceId, "arn": out.ARN, "endpoint": out.Endpoint, "readerEndpoint": out.ReaderEndpoint, "port": out.Port, "engine": out.Engine, "engineVersion": out.EngineVersion, "status": out.Status}, nil
}

// Plan compares the desired AuroraCluster spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *AuroraClusterAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[auroracluster.AuroraClusterSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[auroracluster.AuroraClusterOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("AuroraCluster Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	planningAPI, err := a.planningAPI(ctx, account)
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

// BuildImportKey derives the canonical Restate object key for importing
// an existing AuroraCluster resource by its region and provider-native ID.
func (a *AuroraClusterAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing AuroraCluster resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
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

// decodeSpec unmarshals the raw JSON spec from a resource document into
// the typed AuroraCluster spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
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

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *AuroraClusterAdapter) planningAPI(ctx restate.Context, account string) (auroracluster.AuroraClusterAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("AuroraCluster adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve AuroraCluster planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
