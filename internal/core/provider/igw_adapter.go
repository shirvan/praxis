package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/drivers/igw"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type IGWAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI igw.IGWAPI
	apiFactory        func(aws.Config) igw.IGWAPI
}

func NewIGWAdapter() *IGWAdapter {
	return NewIGWAdapterWithRegistry(auth.LoadFromEnv())
}

func NewIGWAdapterWithRegistry(accounts *auth.Registry) *IGWAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &IGWAdapter{
		auth: accounts,
		apiFactory: func(cfg aws.Config) igw.IGWAPI {
			return igw.NewIGWAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

func NewIGWAdapterWithAPI(api igw.IGWAPI) *IGWAdapter {
	return &IGWAdapter{staticPlanningAPI: api}
}

func (a *IGWAdapter) Kind() string {
	return igw.ServiceName
}

func (a *IGWAdapter) ServiceName() string {
	return igw.ServiceName
}

func (a *IGWAdapter) Scope() KeyScope {
	return KeyScopeRegion
}

func (a *IGWAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("internet gateway name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

func (a *IGWAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *IGWAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[igw.IGWSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[igw.IGWSpec, igw.IGWOutputs](
		restate.Object[igw.IGWOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[igw.IGWOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *IGWAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})

	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *IGWAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[igw.IGWOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"internetGatewayId": out.InternetGatewayId,
		"vpcId":             out.VpcId,
		"ownerId":           out.OwnerId,
		"state":             out.State,
	}, nil
}

func (a *IGWAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[igw.IGWSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[igw.IGWOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("InternetGateway Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.InternetGatewayId == "" {
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
		State igw.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeInternetGateway(runCtx, outputs.InternetGatewayId)
		if descErr != nil {
			if igw.IsNotFound(descErr) {
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

	rawDiffs := igw.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}

	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *IGWAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *IGWAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, igw.IGWOutputs](
		restate.Object[igw.IGWOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *IGWAdapter) decodeSpec(doc resourceDocument) (igw.IGWSpec, error) {
	var spec igw.IGWSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return igw.IGWSpec{}, fmt.Errorf("decode InternetGateway spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return igw.IGWSpec{}, fmt.Errorf("InternetGateway metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return igw.IGWSpec{}, fmt.Errorf("InternetGateway spec.region is required")
	}
	if strings.TrimSpace(spec.VpcId) == "" {
		return igw.IGWSpec{}, fmt.Errorf("InternetGateway spec.vpcId is required")
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

func (a *IGWAdapter) planningAPI(account string) (igw.IGWAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("InternetGateway adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve InternetGateway planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
