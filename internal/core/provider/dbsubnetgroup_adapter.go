// DBSubnetGroup provider adapter.
//
// This file implements the provider.Adapter interface for Amazon RDS (DB Subnet Group)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the DBSubnetGroup Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + subnet group name.
// DB subnet groups are region-scoped; the key combines the AWS region and subnet group name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/dbsubnetgroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// DBSubnetGroupAdapter implements provider.Adapter for DBSubnetGroup (Amazon RDS (DB Subnet Group)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type DBSubnetGroupAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI dbsubnetgroup.DBSubnetGroupAPI
	apiFactory        func(aws.Config) dbsubnetgroup.DBSubnetGroupAPI
}

// NewDBSubnetGroupAdapterWithAuth creates a production DBSubnetGroup adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewDBSubnetGroupAdapterWithAuth(auth authservice.AuthClient) *DBSubnetGroupAdapter {
	return &DBSubnetGroupAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) dbsubnetgroup.DBSubnetGroupAPI {
			return dbsubnetgroup.NewDBSubnetGroupAPI(awsclient.NewRDSClient(cfg))
		},
	}
}

// NewDBSubnetGroupAdapterWithAPI creates a DBSubnetGroup adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewDBSubnetGroupAdapterWithAPI(api dbsubnetgroup.DBSubnetGroupAPI) *DBSubnetGroupAdapter {
	return &DBSubnetGroupAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "DBSubnetGroup" that maps template
// resource documents to this adapter in the provider registry.
func (a *DBSubnetGroupAdapter) Kind() string        { return dbsubnetgroup.ServiceName }
// ServiceName returns the Restate Virtual Object service name for the
// DBSubnetGroup driver. The orchestrator uses this to dispatch durable RPCs.
func (a *DBSubnetGroupAdapter) ServiceName() string { return dbsubnetgroup.ServiceName }
// Scope returns the key-scope strategy for DBSubnetGroup resources,
// which controls how BuildKey assembles the canonical object key.
func (a *DBSubnetGroupAdapter) Scope() KeyScope     { return KeyScopeRegion }

// BuildKey derives the canonical Restate object key for a DBSubnetGroup resource
// from the raw JSON resource document. The key is composed of region + subnet group name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *DBSubnetGroupAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("db subnet group name", spec.GroupName); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.GroupName), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete DBSubnetGroup spec struct expected by the driver.
func (a *DBSubnetGroupAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the DBSubnetGroup Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *DBSubnetGroupAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[dbsubnetgroup.DBSubnetGroupSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[dbsubnetgroup.DBSubnetGroupSpec, dbsubnetgroup.DBSubnetGroupOutputs](restate.Object[dbsubnetgroup.DBSubnetGroupOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[dbsubnetgroup.DBSubnetGroupOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the DBSubnetGroup Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *DBSubnetGroupAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed DBSubnetGroup driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *DBSubnetGroupAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[dbsubnetgroup.DBSubnetGroupOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"groupName":         out.GroupName,
		"arn":               out.ARN,
		"vpcId":             out.VpcId,
		"subnetIds":         out.SubnetIds,
		"availabilityZones": out.AvailabilityZones,
		"status":            out.Status,
	}, nil
}

// Plan compares the desired DBSubnetGroup spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *DBSubnetGroupAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[dbsubnetgroup.DBSubnetGroupSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[dbsubnetgroup.DBSubnetGroupOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("DBSubnetGroup Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}
	type describePlanResult struct {
		State dbsubnetgroup.ObservedState
		Found bool
	}
	groupName := outputs.GroupName
	if groupName == "" {
		groupName = desired.GroupName
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeDBSubnetGroup(runCtx, groupName)
		if descErr != nil {
			if dbsubnetgroup.IsNotFound(descErr) {
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
	rawDiffs := dbsubnetgroup.ComputeFieldDiffs(desired, result.State)
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
// an existing DBSubnetGroup resource by its region and provider-native ID.
func (a *DBSubnetGroupAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing DBSubnetGroup resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *DBSubnetGroupAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, dbsubnetgroup.DBSubnetGroupOutputs](restate.Object[dbsubnetgroup.DBSubnetGroupOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
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
// the typed DBSubnetGroup spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *DBSubnetGroupAdapter) decodeSpec(doc resourceDocument) (dbsubnetgroup.DBSubnetGroupSpec, error) {
	var spec struct {
		Region      string            `json:"region"`
		Description string            `json:"description"`
		SubnetIds   []string          `json:"subnetIds"`
		Tags        map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return dbsubnetgroup.DBSubnetGroupSpec{}, fmt.Errorf("decode DBSubnetGroup spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return dbsubnetgroup.DBSubnetGroupSpec{}, fmt.Errorf("DBSubnetGroup metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return dbsubnetgroup.DBSubnetGroupSpec{}, fmt.Errorf("DBSubnetGroup spec.region is required")
	}
	return dbsubnetgroup.DBSubnetGroupSpec{Region: strings.TrimSpace(spec.Region), GroupName: name, Description: spec.Description, SubnetIds: spec.SubnetIds, Tags: spec.Tags}, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *DBSubnetGroupAdapter) planningAPI(ctx restate.Context, account string) (dbsubnetgroup.DBSubnetGroupAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("DBSubnetGroup adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve DBSubnetGroup planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
