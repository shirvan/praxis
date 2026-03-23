package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/internal/drivers/lambda"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

type LambdaAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI lambda.LambdaAPI
	apiFactory        func(aws.Config) lambda.LambdaAPI
}

func NewLambdaAdapter() *LambdaAdapter {
	return NewLambdaAdapterWithRegistry(auth.LoadFromEnv())
}

func NewLambdaAdapterWithRegistry(accounts *auth.Registry) *LambdaAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &LambdaAdapter{auth: accounts, apiFactory: func(cfg aws.Config) lambda.LambdaAPI { return lambda.NewLambdaAPI(awsclient.NewLambdaClient(cfg)) }}
}

func NewLambdaAdapterWithAPI(api lambda.LambdaAPI) *LambdaAdapter {
	return &LambdaAdapter{staticPlanningAPI: api}
}

func (a *LambdaAdapter) Kind() string { return lambda.ServiceName }
func (a *LambdaAdapter) ServiceName() string { return lambda.ServiceName }
func (a *LambdaAdapter) Scope() KeyScope { return KeyScopeRegion }

func (a *LambdaAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("Lambda function name", spec.FunctionName); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.FunctionName), nil
}

func (a *LambdaAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *LambdaAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[lambda.LambdaFunctionSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[lambda.LambdaFunctionSpec, lambda.LambdaFunctionOutputs](restate.Object[lambda.LambdaFunctionOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[lambda.LambdaFunctionOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *LambdaAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *LambdaAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[lambda.LambdaFunctionOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"functionArn": out.FunctionArn, "functionName": out.FunctionName}
	if out.Version != "" {
		result["version"] = out.Version
	}
	if out.State != "" {
		result["state"] = out.State
	}
	if out.LastModified != "" {
		result["lastModified"] = out.LastModified
	}
	if out.LastUpdateStatus != "" {
		result["lastUpdateStatus"] = out.LastUpdateStatus
	}
	if out.CodeSha256 != "" {
		result["codeSha256"] = out.CodeSha256
	}
	return result, nil
}

func (a *LambdaAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[lambda.LambdaFunctionSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[lambda.LambdaFunctionOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("LambdaFunction Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.FunctionArn == "" {
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
		State lambda.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeFunction(runCtx, outputs.FunctionName)
		if descErr != nil {
			if lambda.IsNotFound(descErr) {
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
	rawDiffs := lambda.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *LambdaAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *LambdaAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, lambda.LambdaFunctionOutputs](restate.Object[lambda.LambdaFunctionOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *LambdaAdapter) decodeSpec(doc resourceDocument) (lambda.LambdaFunctionSpec, error) {
	var spec lambda.LambdaFunctionSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return lambda.LambdaFunctionSpec{}, fmt.Errorf("decode LambdaFunction spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return lambda.LambdaFunctionSpec{}, fmt.Errorf("LambdaFunction metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return lambda.LambdaFunctionSpec{}, fmt.Errorf("LambdaFunction spec.region is required")
	}
	spec.FunctionName = name
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec, nil
}

func (a *LambdaAdapter) planningAPI(account string) (lambda.LambdaAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("Lambda adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve Lambda planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}