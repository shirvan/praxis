package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/internal/drivers/route53healthcheck"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

type Route53HealthCheckAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI route53healthcheck.HealthCheckAPI
	apiFactory        func(aws.Config) route53healthcheck.HealthCheckAPI
}

func NewRoute53HealthCheckAdapter() *Route53HealthCheckAdapter {
	return NewRoute53HealthCheckAdapterWithRegistry(auth.LoadFromEnv())
}

func NewRoute53HealthCheckAdapterWithRegistry(accounts *auth.Registry) *Route53HealthCheckAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &Route53HealthCheckAdapter{
		auth: accounts,
		apiFactory: func(cfg aws.Config) route53healthcheck.HealthCheckAPI {
			return route53healthcheck.NewHealthCheckAPI(awsclient.NewRoute53Client(cfg))
		},
	}
}

func NewRoute53HealthCheckAdapterWithAPI(api route53healthcheck.HealthCheckAPI) *Route53HealthCheckAdapter {
	return &Route53HealthCheckAdapter{staticPlanningAPI: api}
}

func (a *Route53HealthCheckAdapter) Kind() string        { return route53healthcheck.ServiceName }
func (a *Route53HealthCheckAdapter) ServiceName() string { return route53healthcheck.ServiceName }
func (a *Route53HealthCheckAdapter) Scope() KeyScope     { return KeyScopeGlobal }

func (a *Route53HealthCheckAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("health check name", strings.TrimSpace(doc.Metadata.Name)); err != nil {
		return "", err
	}
	return strings.TrimSpace(doc.Metadata.Name), nil
}

func (a *Route53HealthCheckAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *Route53HealthCheckAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[route53healthcheck.HealthCheckSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[route53healthcheck.HealthCheckSpec, route53healthcheck.HealthCheckOutputs](restate.Object[route53healthcheck.HealthCheckOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[route53healthcheck.HealthCheckOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *Route53HealthCheckAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *Route53HealthCheckAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[route53healthcheck.HealthCheckOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"healthCheckId": out.HealthCheckId}, nil
}

func (a *Route53HealthCheckAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[route53healthcheck.HealthCheckSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[route53healthcheck.HealthCheckOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("Route53HealthCheck Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.HealthCheckId == "" {
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
		State route53healthcheck.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeHealthCheck(runCtx, outputs.HealthCheckId)
		if descErr != nil {
			if route53healthcheck.IsNotFound(descErr) {
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
	rawDiffs := route53healthcheck.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *Route53HealthCheckAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return strings.TrimSpace(resourceID), nil
}

func (a *Route53HealthCheckAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, route53healthcheck.HealthCheckOutputs](restate.Object[route53healthcheck.HealthCheckOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *Route53HealthCheckAdapter) decodeSpec(doc resourceDocument) (route53healthcheck.HealthCheckSpec, error) {
	var spec route53healthcheck.HealthCheckSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return route53healthcheck.HealthCheckSpec{}, fmt.Errorf("decode Route53HealthCheck spec: %w", err)
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.Account = ""
	return spec, nil
}

func (a *Route53HealthCheckAdapter) planningAPI(account string) (route53healthcheck.HealthCheckAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("Route53HealthCheck adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve Route53HealthCheck planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
