package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/internal/drivers/natgw"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

type NATGatewayAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI natgw.NATGatewayAPI
	apiFactory        func(aws.Config) natgw.NATGatewayAPI
}

func NewNATGatewayAdapter() *NATGatewayAdapter {
	return NewNATGatewayAdapterWithRegistry(auth.LoadFromEnv())
}

func NewNATGatewayAdapterWithRegistry(accounts *auth.Registry) *NATGatewayAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &NATGatewayAdapter{
		auth: accounts,
		apiFactory: func(cfg aws.Config) natgw.NATGatewayAPI {
			return natgw.NewNATGatewayAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

func NewNATGatewayAdapterWithAPI(api natgw.NATGatewayAPI) *NATGatewayAdapter {
	return &NATGatewayAdapter{staticPlanningAPI: api}
}

func (a *NATGatewayAdapter) Kind() string {
	return natgw.ServiceName
}

func (a *NATGatewayAdapter) ServiceName() string {
	return natgw.ServiceName
}

func (a *NATGatewayAdapter) Scope() KeyScope {
	return KeyScopeRegion
}

func (a *NATGatewayAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	name := strings.TrimSpace(doc.Metadata.Name)
	if err := ValidateKeyPart("NAT gateway name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

func (a *NATGatewayAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *NATGatewayAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[natgw.NATGatewaySpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[natgw.NATGatewaySpec, natgw.NATGatewayOutputs](
		restate.Object[natgw.NATGatewayOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[natgw.NATGatewayOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *NATGatewayAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *NATGatewayAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[natgw.NATGatewayOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"natGatewayId":       out.NatGatewayId,
		"subnetId":           out.SubnetId,
		"vpcId":              out.VpcId,
		"connectivityType":   out.ConnectivityType,
		"state":              out.State,
		"privateIp":          out.PrivateIp,
		"networkInterfaceId": out.NetworkInterfaceId,
	}
	if out.PublicIp != "" {
		result["publicIp"] = out.PublicIp
	}
	if out.AllocationId != "" {
		result["allocationId"] = out.AllocationId
	}
	return result, nil
}

func (a *NATGatewayAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[natgw.NATGatewaySpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[natgw.NATGatewayOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("NATGateway Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.NatGatewayId == "" {
		fields, fieldErr := createFieldDiffsFromSpec(desired)
		if fieldErr != nil {
			return "", nil, fieldErr
		}
		return types.OpCreate, fields, nil
	}

	planningAPI, err := a.planningAPI(account)
	if err != nil {
		return "", nil, err
	}

	type describePlanResult struct {
		State natgw.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeNATGateway(runCtx, outputs.NatGatewayId)
		if descErr != nil {
			if natgw.IsNotFound(descErr) {
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

	rawDiffs := natgw.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}

	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *NATGatewayAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *NATGatewayAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, natgw.NATGatewayOutputs](
		restate.Object[natgw.NATGatewayOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *NATGatewayAdapter) decodeSpec(doc resourceDocument) (natgw.NATGatewaySpec, error) {
	var spec natgw.NATGatewaySpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return natgw.NATGatewaySpec{}, fmt.Errorf("decode NATGateway spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return natgw.NATGatewaySpec{}, fmt.Errorf("NATGateway metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return natgw.NATGatewaySpec{}, fmt.Errorf("NATGateway spec.region is required")
	}
	if strings.TrimSpace(spec.SubnetId) == "" {
		return natgw.NATGatewaySpec{}, fmt.Errorf("NATGateway spec.subnetId is required")
	}
	spec = natgw.NATGatewaySpec{
		Account:          "",
		Region:           spec.Region,
		SubnetId:         spec.SubnetId,
		ConnectivityType: spec.ConnectivityType,
		AllocationId:     spec.AllocationId,
		Tags:             spec.Tags,
	}
	spec = natgwSpecWithDefaults(spec)
	if spec.Tags["Name"] == "" {
		spec.Tags["Name"] = name
	}
	if spec.ConnectivityType == "private" && spec.AllocationId != "" {
		return natgw.NATGatewaySpec{}, fmt.Errorf("NATGateway spec.allocationId must be empty for private NAT gateways")
	}
	if spec.ConnectivityType == "public" && strings.TrimSpace(spec.AllocationId) == "" {
		return natgw.NATGatewaySpec{}, fmt.Errorf("NATGateway spec.allocationId is required for public NAT gateways")
	}
	return spec, nil
}

func (a *NATGatewayAdapter) planningAPI(account string) (natgw.NATGatewayAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("NATGateway adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve NATGateway planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}

func natgwSpecWithDefaults(spec natgw.NATGatewaySpec) natgw.NATGatewaySpec {
	if spec.ConnectivityType == "" {
		spec.ConnectivityType = "public"
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	return spec
}
