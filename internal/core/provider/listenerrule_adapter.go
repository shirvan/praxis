package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/listenerrule"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type ListenerRuleAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI listenerrule.ListenerRuleAPI
	apiFactory        func(aws.Config) listenerrule.ListenerRuleAPI
}

func NewListenerRuleAdapterWithAuth(auth authservice.AuthClient) *ListenerRuleAdapter {
	return &ListenerRuleAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) listenerrule.ListenerRuleAPI {
			return listenerrule.NewListenerRuleAPI(awsclient.NewELBv2Client(cfg))
		},
	}
}

func NewListenerRuleAdapterWithAPI(api listenerrule.ListenerRuleAPI) *ListenerRuleAdapter {
	return &ListenerRuleAdapter{staticPlanningAPI: api}
}

func (a *ListenerRuleAdapter) Kind() string        { return listenerrule.ServiceName }
func (a *ListenerRuleAdapter) ServiceName() string { return listenerrule.ServiceName }
func (a *ListenerRuleAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *ListenerRuleAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	region := extractRegionFromListenerArn(spec.ListenerArn)
	if region == "" {
		return "", fmt.Errorf("cannot extract region from listenerArn %q", spec.ListenerArn)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if err := ValidateKeyPart("listener rule name", name); err != nil {
		return "", err
	}
	return JoinKey(region, name), nil
}

func (a *ListenerRuleAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *ListenerRuleAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[listenerrule.ListenerRuleSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[listenerrule.ListenerRuleSpec, listenerrule.ListenerRuleOutputs](restate.Object[listenerrule.ListenerRuleOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[listenerrule.ListenerRuleOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *ListenerRuleAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *ListenerRuleAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[listenerrule.ListenerRuleOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ruleArn":  out.RuleArn,
		"priority": out.Priority,
	}, nil
}

func (a *ListenerRuleAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[listenerrule.ListenerRuleSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[listenerrule.ListenerRuleOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("ListenerRule Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.RuleArn == "" {
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
		State listenerrule.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeRule(runCtx, outputs.RuleArn)
		if descErr != nil {
			if listenerrule.IsNotFound(descErr) {
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
	rawDiffs := listenerrule.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *ListenerRuleAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *ListenerRuleAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, listenerrule.ListenerRuleOutputs](restate.Object[listenerrule.ListenerRuleOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *ListenerRuleAdapter) decodeSpec(doc resourceDocument) (listenerrule.ListenerRuleSpec, error) {
	var spec listenerrule.ListenerRuleSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return listenerrule.ListenerRuleSpec{}, fmt.Errorf("decode ListenerRule spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return listenerrule.ListenerRuleSpec{}, fmt.Errorf("ListenerRule metadata.name is required")
	}
	if spec.ListenerArn == "" {
		return listenerrule.ListenerRuleSpec{}, fmt.Errorf("ListenerRule spec.listenerArn is required")
	}
	spec.Account = ""
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	if spec.Tags["praxis:rule-name"] == "" {
		spec.Tags["praxis:rule-name"] = name
	}
	return spec, nil
}

func (a *ListenerRuleAdapter) planningAPI(ctx restate.Context, account string) (listenerrule.ListenerRuleAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("ListenerRule adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve ListenerRule planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}

func extractRegionFromListenerArn(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 4 {
		return parts[3]
	}
	return ""
}
