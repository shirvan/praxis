// DBParameterGroup provider adapter.
//
// This file implements the provider.Adapter interface for Amazon RDS (DB Parameter Group)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the DBParameterGroup Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + parameter group name.
// DB parameter groups are region-scoped; the key combines the AWS region and parameter group name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/dbparametergroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// DBParameterGroupAdapter implements provider.Adapter for DBParameterGroup (Amazon RDS (DB Parameter Group)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type DBParameterGroupAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI dbparametergroup.DBParameterGroupAPI
	apiFactory        func(aws.Config) dbparametergroup.DBParameterGroupAPI
}

// NewDBParameterGroupAdapterWithAuth creates a production DBParameterGroup adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewDBParameterGroupAdapterWithAuth(auth authservice.AuthClient) *DBParameterGroupAdapter {
	return &DBParameterGroupAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) dbparametergroup.DBParameterGroupAPI {
			return dbparametergroup.NewDBParameterGroupAPI(awsclient.NewRDSClient(cfg))
		},
	}
}

// NewDBParameterGroupAdapterWithAPI creates a DBParameterGroup adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewDBParameterGroupAdapterWithAPI(api dbparametergroup.DBParameterGroupAPI) *DBParameterGroupAdapter {
	return &DBParameterGroupAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "DBParameterGroup" that maps template
// resource documents to this adapter in the provider registry.
func (a *DBParameterGroupAdapter) Kind() string        { return dbparametergroup.ServiceName }
// ServiceName returns the Restate Virtual Object service name for the
// DBParameterGroup driver. The orchestrator uses this to dispatch durable RPCs.
func (a *DBParameterGroupAdapter) ServiceName() string { return dbparametergroup.ServiceName }
// Scope returns the key-scope strategy for DBParameterGroup resources,
// which controls how BuildKey assembles the canonical object key.
func (a *DBParameterGroupAdapter) Scope() KeyScope     { return KeyScopeRegion }

// BuildKey derives the canonical Restate object key for a DBParameterGroup resource
// from the raw JSON resource document. The key is composed of region + parameter group name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *DBParameterGroupAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("db parameter group name", spec.GroupName); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.GroupName), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete DBParameterGroup spec struct expected by the driver.
func (a *DBParameterGroupAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the DBParameterGroup Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *DBParameterGroupAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[dbparametergroup.DBParameterGroupSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs](restate.Object[dbparametergroup.DBParameterGroupOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[dbparametergroup.DBParameterGroupOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the DBParameterGroup Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *DBParameterGroupAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed DBParameterGroup driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *DBParameterGroupAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[dbparametergroup.DBParameterGroupOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"groupName": out.GroupName, "arn": out.ARN, "family": out.Family, "type": out.Type}, nil
}

// Plan compares the desired DBParameterGroup spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *DBParameterGroupAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[dbparametergroup.DBParameterGroupSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[dbparametergroup.DBParameterGroupOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("DBParameterGroup Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}
	type describePlanResult struct {
		State dbparametergroup.ObservedState
		Found bool
	}
	groupName := outputs.GroupName
	if groupName == "" {
		groupName = desired.GroupName
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeParameterGroup(runCtx, groupName, desired.Type)
		if descErr != nil {
			if dbparametergroup.IsNotFound(descErr) {
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
	rawDiffs := dbparametergroup.ComputeFieldDiffs(desired, result.State)
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
// an existing DBParameterGroup resource by its region and provider-native ID.
func (a *DBParameterGroupAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing DBParameterGroup resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *DBParameterGroupAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, dbparametergroup.DBParameterGroupOutputs](restate.Object[dbparametergroup.DBParameterGroupOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
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
// the typed DBParameterGroup spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *DBParameterGroupAdapter) decodeSpec(doc resourceDocument) (dbparametergroup.DBParameterGroupSpec, error) {
	var spec struct {
		Region      string            `json:"region"`
		Type        string            `json:"type"`
		Family      string            `json:"family"`
		Description string            `json:"description"`
		Parameters  map[string]string `json:"parameters"`
		Tags        map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return dbparametergroup.DBParameterGroupSpec{}, fmt.Errorf("decode DBParameterGroup spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return dbparametergroup.DBParameterGroupSpec{}, fmt.Errorf("DBParameterGroup metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return dbparametergroup.DBParameterGroupSpec{}, fmt.Errorf("DBParameterGroup spec.region is required")
	}
	return dbparametergroup.DBParameterGroupSpec{Region: strings.TrimSpace(spec.Region), GroupName: name, Type: strings.TrimSpace(spec.Type), Family: spec.Family, Description: spec.Description, Parameters: spec.Parameters, Tags: spec.Tags}, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *DBParameterGroupAdapter) planningAPI(ctx restate.Context, account string) (dbparametergroup.DBParameterGroupAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("DBParameterGroup adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve DBParameterGroup planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
