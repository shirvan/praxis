package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/drivers/lambdaperm"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type LambdaPermissionAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI lambdaperm.PermissionAPI
	apiFactory        func(aws.Config) lambdaperm.PermissionAPI
}

func NewLambdaPermissionAdapter() *LambdaPermissionAdapter {
	return NewLambdaPermissionAdapterWithRegistry(auth.LoadFromEnv())
}

func NewLambdaPermissionAdapterWithRegistry(accounts *auth.Registry) *LambdaPermissionAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &LambdaPermissionAdapter{auth: accounts, apiFactory: func(cfg aws.Config) lambdaperm.PermissionAPI {
		return lambdaperm.NewPermissionAPI(awsclient.NewLambdaClient(cfg))
	}}
}

func (a *LambdaPermissionAdapter) Kind() string        { return lambdaperm.ServiceName }
func (a *LambdaPermissionAdapter) ServiceName() string { return lambdaperm.ServiceName }
func (a *LambdaPermissionAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *LambdaPermissionAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("statement ID", spec.StatementId); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.FunctionName, spec.StatementId), nil
}

func (a *LambdaPermissionAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *LambdaPermissionAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[lambdaperm.LambdaPermissionSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[lambdaperm.LambdaPermissionSpec, lambdaperm.LambdaPermissionOutputs](restate.Object[lambdaperm.LambdaPermissionOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[lambdaperm.LambdaPermissionOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *LambdaPermissionAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *LambdaPermissionAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[lambdaperm.LambdaPermissionOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"statementId": out.StatementId, "functionName": out.FunctionName, "statement": out.Statement}, nil
}

func (a *LambdaPermissionAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[lambdaperm.LambdaPermissionSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[lambdaperm.LambdaPermissionOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("LambdaPermission Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.StatementId == "" {
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
		State lambdaperm.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.GetPermission(runCtx, outputs.FunctionName, outputs.StatementId)
		if descErr != nil {
			if lambdaperm.IsNotFound(descErr) {
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
	rawDiffs := lambdaperm.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *LambdaPermissionAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	functionName, statementID, err := lambdapermSplitResourceID(resourceID)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("function name", functionName); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("statement ID", statementID); err != nil {
		return "", err
	}
	return JoinKey(region, functionName, statementID), nil
}

func (a *LambdaPermissionAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, lambdaperm.LambdaPermissionOutputs](restate.Object[lambdaperm.LambdaPermissionOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *LambdaPermissionAdapter) decodeSpec(doc resourceDocument) (lambdaperm.LambdaPermissionSpec, error) {
	var spec lambdaperm.LambdaPermissionSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return lambdaperm.LambdaPermissionSpec{}, fmt.Errorf("decode LambdaPermission spec: %w", err)
	}
	if strings.TrimSpace(spec.Region) == "" {
		return lambdaperm.LambdaPermissionSpec{}, fmt.Errorf("LambdaPermission spec.region is required")
	}
	if strings.TrimSpace(spec.StatementId) == "" {
		spec.StatementId = strings.TrimSpace(doc.Metadata.Name)
	}
	if strings.TrimSpace(spec.StatementId) == "" {
		return lambdaperm.LambdaPermissionSpec{}, fmt.Errorf("LambdaPermission metadata.name or spec.statementId is required")
	}
	return lambdaperm.LambdaPermissionSpec{Region: spec.Region, FunctionName: spec.FunctionName, StatementId: spec.StatementId, Action: spec.Action, Principal: spec.Principal, SourceArn: spec.SourceArn, SourceAccount: spec.SourceAccount, EventSourceToken: spec.EventSourceToken, Qualifier: spec.Qualifier}, nil
}

func (a *LambdaPermissionAdapter) planningAPI(account string) (lambdaperm.PermissionAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("Lambda permission adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve Lambda permission planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}

func lambdapermSplitResourceID(resourceID string) (string, string, error) {
	parts := strings.SplitN(resourceID, "~", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("import resource ID must be functionName~statementId")
	}
	return parts[0], parts[1], nil
}
