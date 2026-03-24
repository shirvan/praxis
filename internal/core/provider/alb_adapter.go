package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/alb"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type ALBAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI alb.ALBAPI
	apiFactory        func(aws.Config) alb.ALBAPI
}

func NewALBAdapterWithAuth(auth authservice.AuthClient) *ALBAdapter {
	return &ALBAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) alb.ALBAPI {
			return alb.NewALBAPI(awsclient.NewELBv2Client(cfg))
		},
	}
}

func NewALBAdapterWithAPI(api alb.ALBAPI) *ALBAdapter {
	return &ALBAdapter{staticPlanningAPI: api}
}

func (a *ALBAdapter) Kind() string        { return alb.ServiceName }
func (a *ALBAdapter) ServiceName() string { return alb.ServiceName }
func (a *ALBAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *ALBAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("ALB name", spec.Name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.Name), nil
}

func (a *ALBAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *ALBAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[alb.ALBSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[alb.ALBSpec, alb.ALBOutputs](restate.Object[alb.ALBOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[alb.ALBOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *ALBAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *ALBAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[alb.ALBOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"loadBalancerArn":       out.LoadBalancerArn,
		"dnsName":               out.DnsName,
		"hostedZoneId":          out.HostedZoneId,
		"vpcId":                 out.VpcId,
		"canonicalHostedZoneId": out.CanonicalHostedZoneId,
	}, nil
}

func (a *ALBAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[alb.ALBSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[alb.ALBOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("ALB Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}
	identifier := outputs.LoadBalancerArn
	if identifier == "" {
		identifier = desired.Name
	}
	type describePlanResult struct {
		State alb.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeALB(runCtx, identifier)
		if descErr != nil {
			if alb.IsNotFound(descErr) {
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
	rawDiffs := alb.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *ALBAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *ALBAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, alb.ALBOutputs](restate.Object[alb.ALBOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *ALBAdapter) decodeSpec(doc resourceDocument) (alb.ALBSpec, error) {
	var spec alb.ALBSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return alb.ALBSpec{}, fmt.Errorf("decode ALB spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return alb.ALBSpec{}, fmt.Errorf("ALB metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return alb.ALBSpec{}, fmt.Errorf("ALB spec.region is required")
	}
	spec.Name = name
	spec.Account = ""
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	if spec.Tags["Name"] == "" {
		spec.Tags["Name"] = name
	}
	return spec, nil
}

func (a *ALBAdapter) planningAPI(ctx restate.Context, account string) (alb.ALBAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("ALB adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve ALB planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
