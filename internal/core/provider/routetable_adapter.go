package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/routetable"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type RouteTableAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI routetable.RouteTableAPI
	apiFactory        func(aws.Config) routetable.RouteTableAPI
}

func NewRouteTableAdapterWithAuth(auth authservice.AuthClient) *RouteTableAdapter {
	return &RouteTableAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) routetable.RouteTableAPI {
			return routetable.NewRouteTableAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

func NewRouteTableAdapterWithAPI(api routetable.RouteTableAPI) *RouteTableAdapter {
	return &RouteTableAdapter{staticPlanningAPI: api}
}

func (a *RouteTableAdapter) Kind() string {
	return routetable.ServiceName
}

func (a *RouteTableAdapter) ServiceName() string {
	return routetable.ServiceName
}

func (a *RouteTableAdapter) Scope() KeyScope {
	return KeyScopeCustom
}

func (a *RouteTableAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("VPC ID", spec.VpcId); err != nil {
		return "", err
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if err := ValidateKeyPart("route table name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.VpcId, name), nil
}

func (a *RouteTableAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *RouteTableAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[routetable.RouteTableSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[routetable.RouteTableSpec, routetable.RouteTableOutputs](
		restate.Object[routetable.RouteTableOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[routetable.RouteTableOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *RouteTableAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *RouteTableAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[routetable.RouteTableOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"routeTableId": out.RouteTableId,
		"vpcId":        out.VpcId,
		"ownerId":      out.OwnerId,
		"routes":       out.Routes,
		"associations": out.Associations,
	}, nil
}

func (a *RouteTableAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[routetable.RouteTableSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[routetable.RouteTableOutputs](ctx, a.ServiceName(), key, "GetOutputs").
		Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("RouteTable Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.RouteTableId == "" {
		fields, fieldErr := createFieldDiffsFromSpec(desired)
		if fieldErr != nil {
			return "", nil, fieldErr
		}
		return types.OpCreate, fields, nil
	}

	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}

	type describePlanResult struct {
		State routetable.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeRouteTable(runCtx, outputs.RouteTableId)
		if descErr != nil {
			if routetable.IsNotFound(descErr) {
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

	rawDiffs := routetable.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *RouteTableAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *RouteTableAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, routetable.RouteTableOutputs](
		restate.Object[routetable.RouteTableOutputs](ctx, a.ServiceName(), key, "Import"),
	).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *RouteTableAdapter) decodeSpec(doc resourceDocument) (routetable.RouteTableSpec, error) {
	var spec routetable.RouteTableSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return routetable.RouteTableSpec{}, fmt.Errorf("decode RouteTable spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return routetable.RouteTableSpec{}, fmt.Errorf("RouteTable metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return routetable.RouteTableSpec{}, fmt.Errorf("RouteTable spec.region is required")
	}
	if strings.TrimSpace(spec.VpcId) == "" {
		return routetable.RouteTableSpec{}, fmt.Errorf("RouteTable spec.vpcId is required")
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	if spec.Tags["Name"] == "" {
		spec.Tags["Name"] = name
	}
	spec.Account = ""
	return spec, nil
}

func (a *RouteTableAdapter) planningAPI(ctx restate.Context, account string) (routetable.RouteTableAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("RouteTable adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve RouteTable planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
