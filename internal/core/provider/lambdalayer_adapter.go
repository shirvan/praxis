package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/lambdalayer"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type LambdaLayerAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI lambdalayer.LayerAPI
	apiFactory        func(aws.Config) lambdalayer.LayerAPI
}

func NewLambdaLayerAdapterWithAuth(auth authservice.AuthClient) *LambdaLayerAdapter {
	return &LambdaLayerAdapter{auth: auth, apiFactory: func(cfg aws.Config) lambdalayer.LayerAPI {
		return lambdalayer.NewLayerAPI(awsclient.NewLambdaClient(cfg))
	}}
}

func NewLambdaLayerAdapterWithAPI(api lambdalayer.LayerAPI) *LambdaLayerAdapter {
	return &LambdaLayerAdapter{staticPlanningAPI: api}
}

func (a *LambdaLayerAdapter) Kind() string        { return lambdalayer.ServiceName }
func (a *LambdaLayerAdapter) ServiceName() string { return lambdalayer.ServiceName }
func (a *LambdaLayerAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *LambdaLayerAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("Lambda layer name", spec.LayerName); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.LayerName), nil
}

func (a *LambdaLayerAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *LambdaLayerAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[lambdalayer.LambdaLayerSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[lambdalayer.LambdaLayerSpec, lambdalayer.LambdaLayerOutputs](restate.Object[lambdalayer.LambdaLayerOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[lambdalayer.LambdaLayerOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *LambdaLayerAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *LambdaLayerAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[lambdalayer.LambdaLayerOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"layerArn": out.LayerArn, "layerVersionArn": out.LayerVersionArn, "layerName": out.LayerName, "version": out.Version}
	if out.CodeSize > 0 {
		result["codeSize"] = out.CodeSize
	}
	if out.CodeSha256 != "" {
		result["codeSha256"] = out.CodeSha256
	}
	if out.CreatedDate != "" {
		result["createdDate"] = out.CreatedDate
	}
	return result, nil
}

func (a *LambdaLayerAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[lambdalayer.LambdaLayerSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[lambdalayer.LambdaLayerOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("LambdaLayer Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.LayerVersionArn == "" {
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
		State lambdalayer.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.GetLatestLayerVersion(runCtx, outputs.LayerName)
		if descErr != nil {
			if lambdalayer.IsNotFound(descErr) {
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
	rawDiffs := lambdalayer.ComputeFieldDiffs(desired, result.State, outputs)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *LambdaLayerAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *LambdaLayerAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, lambdalayer.LambdaLayerOutputs](restate.Object[lambdalayer.LambdaLayerOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *LambdaLayerAdapter) decodeSpec(doc resourceDocument) (lambdalayer.LambdaLayerSpec, error) {
	var spec lambdalayer.LambdaLayerSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return lambdalayer.LambdaLayerSpec{}, fmt.Errorf("decode LambdaLayer spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return lambdalayer.LambdaLayerSpec{}, fmt.Errorf("LambdaLayer metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return lambdalayer.LambdaLayerSpec{}, fmt.Errorf("LambdaLayer spec.region is required")
	}
	spec.LayerName = name
	return spec, nil
}

func (a *LambdaLayerAdapter) planningAPI(ctx restate.Context, account string) (lambdalayer.LayerAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("lambda layer adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Lambda layer planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
