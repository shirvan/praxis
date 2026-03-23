package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/drivers/dbsubnetgroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type DBSubnetGroupAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI dbsubnetgroup.DBSubnetGroupAPI
	apiFactory        func(aws.Config) dbsubnetgroup.DBSubnetGroupAPI
}

func NewDBSubnetGroupAdapter() *DBSubnetGroupAdapter {
	return NewDBSubnetGroupAdapterWithRegistry(auth.LoadFromEnv())
}

func NewDBSubnetGroupAdapterWithRegistry(accounts *auth.Registry) *DBSubnetGroupAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &DBSubnetGroupAdapter{
		auth: accounts,
		apiFactory: func(cfg aws.Config) dbsubnetgroup.DBSubnetGroupAPI {
			return dbsubnetgroup.NewDBSubnetGroupAPI(awsclient.NewRDSClient(cfg))
		},
	}
}

func NewDBSubnetGroupAdapterWithAPI(api dbsubnetgroup.DBSubnetGroupAPI) *DBSubnetGroupAdapter {
	return &DBSubnetGroupAdapter{staticPlanningAPI: api}
}

func (a *DBSubnetGroupAdapter) Kind() string        { return dbsubnetgroup.ServiceName }
func (a *DBSubnetGroupAdapter) ServiceName() string { return dbsubnetgroup.ServiceName }
func (a *DBSubnetGroupAdapter) Scope() KeyScope     { return KeyScopeRegion }

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

func (a *DBSubnetGroupAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *DBSubnetGroupAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[dbsubnetgroup.DBSubnetGroupSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[dbsubnetgroup.DBSubnetGroupSpec, dbsubnetgroup.DBSubnetGroupOutputs](restate.Object[dbsubnetgroup.DBSubnetGroupOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[dbsubnetgroup.DBSubnetGroupOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *DBSubnetGroupAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

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

func (a *DBSubnetGroupAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[dbsubnetgroup.DBSubnetGroupSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[dbsubnetgroup.DBSubnetGroupOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("DBSubnetGroup Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	planningAPI, err := a.planningAPI(account)
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

func (a *DBSubnetGroupAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

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

func (a *DBSubnetGroupAdapter) planningAPI(account string) (dbsubnetgroup.DBSubnetGroupAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("DBSubnetGroup adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve DBSubnetGroup planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
