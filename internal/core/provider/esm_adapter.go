package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/internal/drivers/esm"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

type ESMAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI esm.ESMAPI
	apiFactory        func(aws.Config) esm.ESMAPI
}

func NewESMAdapter() *ESMAdapter {
	return NewESMAdapterWithRegistry(auth.LoadFromEnv())
}

func NewESMAdapterWithRegistry(accounts *auth.Registry) *ESMAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &ESMAdapter{auth: accounts, apiFactory: func(cfg aws.Config) esm.ESMAPI { return esm.NewESMAPI(awsclient.NewLambdaClient(cfg)) }}
}

func (a *ESMAdapter) Kind() string        { return esm.ServiceName }
func (a *ESMAdapter) ServiceName() string { return esm.ServiceName }
func (a *ESMAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *ESMAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("function name", spec.FunctionName); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.FunctionName, esm.EncodedEventSourceKey(spec.EventSourceArn)), nil
}

func (a *ESMAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *ESMAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[esm.EventSourceMappingSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[esm.EventSourceMappingSpec, esm.EventSourceMappingOutputs](restate.Object[esm.EventSourceMappingOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[esm.EventSourceMappingOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *ESMAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *ESMAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[esm.EventSourceMappingOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"uuid": out.UUID, "eventSourceArn": out.EventSourceArn, "functionArn": out.FunctionArn, "state": out.State, "lastModified": out.LastModified, "batchSize": out.BatchSize}, nil
}

func (a *ESMAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[esm.EventSourceMappingSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[esm.EventSourceMappingOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("EventSourceMapping Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.UUID == "" {
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
		State esm.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.GetEventSourceMapping(runCtx, outputs.UUID)
		if descErr != nil {
			if esm.IsNotFound(descErr) {
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
	rawDiffs := esm.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *ESMAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *ESMAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, esm.EventSourceMappingOutputs](restate.Object[esm.EventSourceMappingOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *ESMAdapter) decodeSpec(doc resourceDocument) (esm.EventSourceMappingSpec, error) {
	var spec esm.EventSourceMappingSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return esm.EventSourceMappingSpec{}, fmt.Errorf("decode EventSourceMapping spec: %w", err)
	}
	if strings.TrimSpace(spec.Region) == "" {
		return esm.EventSourceMappingSpec{}, fmt.Errorf("EventSourceMapping spec.region is required")
	}
	return spec, nil
}

func (a *ESMAdapter) planningAPI(account string) (esm.ESMAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("event source mapping adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve event source mapping planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
