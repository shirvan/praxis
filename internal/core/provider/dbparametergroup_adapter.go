package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/drivers/dbparametergroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type DBParameterGroupAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI dbparametergroup.DBParameterGroupAPI
	apiFactory        func(aws.Config) dbparametergroup.DBParameterGroupAPI
}

func NewDBParameterGroupAdapter() *DBParameterGroupAdapter {
	return NewDBParameterGroupAdapterWithRegistry(auth.LoadFromEnv())
}

func NewDBParameterGroupAdapterWithRegistry(accounts *auth.Registry) *DBParameterGroupAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &DBParameterGroupAdapter{
		auth: accounts,
		apiFactory: func(cfg aws.Config) dbparametergroup.DBParameterGroupAPI {
			return dbparametergroup.NewDBParameterGroupAPI(awsclient.NewRDSClient(cfg))
		},
	}
}

func NewDBParameterGroupAdapterWithAPI(api dbparametergroup.DBParameterGroupAPI) *DBParameterGroupAdapter {
	return &DBParameterGroupAdapter{staticPlanningAPI: api}
}

func (a *DBParameterGroupAdapter) Kind() string        { return dbparametergroup.ServiceName }
func (a *DBParameterGroupAdapter) ServiceName() string { return dbparametergroup.ServiceName }
func (a *DBParameterGroupAdapter) Scope() KeyScope     { return KeyScopeRegion }

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

func (a *DBParameterGroupAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *DBParameterGroupAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[dbparametergroup.DBParameterGroupSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs](restate.Object[dbparametergroup.DBParameterGroupOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[dbparametergroup.DBParameterGroupOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *DBParameterGroupAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *DBParameterGroupAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[dbparametergroup.DBParameterGroupOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"groupName": out.GroupName, "arn": out.ARN, "family": out.Family, "type": out.Type}, nil
}

func (a *DBParameterGroupAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[dbparametergroup.DBParameterGroupSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[dbparametergroup.DBParameterGroupOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("DBParameterGroup Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	planningAPI, err := a.planningAPI(account)
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

func (a *DBParameterGroupAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

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

func (a *DBParameterGroupAdapter) planningAPI(account string) (dbparametergroup.DBParameterGroupAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("DBParameterGroup adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve DBParameterGroup planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
