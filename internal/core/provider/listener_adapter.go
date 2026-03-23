package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/internal/drivers/listener"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

type ListenerAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI listener.ListenerAPI
	apiFactory        func(aws.Config) listener.ListenerAPI
}

func NewListenerAdapter() *ListenerAdapter { return NewListenerAdapterWithRegistry(auth.LoadFromEnv()) }

func NewListenerAdapterWithRegistry(accounts *auth.Registry) *ListenerAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &ListenerAdapter{
		auth: accounts,
		apiFactory: func(cfg aws.Config) listener.ListenerAPI {
			return listener.NewListenerAPI(awsclient.NewELBv2Client(cfg))
		},
	}
}

func NewListenerAdapterWithAPI(api listener.ListenerAPI) *ListenerAdapter {
	return &ListenerAdapter{staticPlanningAPI: api}
}

func (a *ListenerAdapter) Kind() string        { return listener.ServiceName }
func (a *ListenerAdapter) ServiceName() string { return listener.ServiceName }
func (a *ListenerAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *ListenerAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	region := extractRegionFromLBArn(spec.LoadBalancerArn)
	if region == "" {
		return "", fmt.Errorf("cannot extract region from loadBalancerArn %q", spec.LoadBalancerArn)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if err := ValidateKeyPart("listener name", name); err != nil {
		return "", err
	}
	return JoinKey(region, name), nil
}

func (a *ListenerAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *ListenerAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[listener.ListenerSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[listener.ListenerSpec, listener.ListenerOutputs](restate.Object[listener.ListenerOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[listener.ListenerOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *ListenerAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *ListenerAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[listener.ListenerOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"listenerArn": out.ListenerArn,
		"port":        out.Port,
		"protocol":    out.Protocol,
	}, nil
}

func (a *ListenerAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[listener.ListenerSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[listener.ListenerOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("Listener Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.ListenerArn == "" {
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
		State listener.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeListener(runCtx, outputs.ListenerArn)
		if descErr != nil {
			if listener.IsNotFound(descErr) {
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
	rawDiffs := listener.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *ListenerAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *ListenerAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, listener.ListenerOutputs](restate.Object[listener.ListenerOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *ListenerAdapter) decodeSpec(doc resourceDocument) (listener.ListenerSpec, error) {
	var spec listener.ListenerSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return listener.ListenerSpec{}, fmt.Errorf("decode Listener spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return listener.ListenerSpec{}, fmt.Errorf("Listener metadata.name is required")
	}
	if spec.LoadBalancerArn == "" {
		return listener.ListenerSpec{}, fmt.Errorf("Listener spec.loadBalancerArn is required")
	}
	spec.Account = ""
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	if spec.Tags["praxis:listener-name"] == "" {
		spec.Tags["praxis:listener-name"] = name
	}
	return spec, nil
}

func (a *ListenerAdapter) planningAPI(account string) (listener.ListenerAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("Listener adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve Listener planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}

func extractRegionFromLBArn(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 4 {
		return parts[3]
	}
	return ""
}
