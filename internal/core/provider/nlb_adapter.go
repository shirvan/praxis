package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/nlb"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type NLBAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI nlb.NLBAPI
	apiFactory        func(aws.Config) nlb.NLBAPI
}

func NewNLBAdapterWithAuth(auth authservice.AuthClient) *NLBAdapter {
	return &NLBAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) nlb.NLBAPI {
			return nlb.NewNLBAPI(awsclient.NewELBv2Client(cfg))
		},
	}
}

func NewNLBAdapterWithAPI(api nlb.NLBAPI) *NLBAdapter {
	return &NLBAdapter{staticPlanningAPI: api}
}

func (a *NLBAdapter) Kind() string        { return nlb.ServiceName }
func (a *NLBAdapter) ServiceName() string { return nlb.ServiceName }
func (a *NLBAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *NLBAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("NLB name", spec.Name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.Name), nil
}

func (a *NLBAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *NLBAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[nlb.NLBSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[nlb.NLBSpec, nlb.NLBOutputs](restate.Object[nlb.NLBOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[nlb.NLBOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *NLBAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *NLBAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[nlb.NLBOutputs](raw)
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

func (a *NLBAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[nlb.NLBSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[nlb.NLBOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("NLB Plan: failed to read outputs for key %q: %w", key, getErr)
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
		State nlb.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeNLB(runCtx, identifier)
		if descErr != nil {
			if nlb.IsNotFound(descErr) {
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
	rawDiffs := nlb.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *NLBAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *NLBAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, nlb.NLBOutputs](restate.Object[nlb.NLBOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *NLBAdapter) decodeSpec(doc resourceDocument) (nlb.NLBSpec, error) {
	var spec nlb.NLBSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return nlb.NLBSpec{}, fmt.Errorf("decode NLB spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return nlb.NLBSpec{}, fmt.Errorf("NLB metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return nlb.NLBSpec{}, fmt.Errorf("NLB spec.region is required")
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

func (a *NLBAdapter) planningAPI(ctx restate.Context, account string) (nlb.NLBAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("NLB adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve NLB planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
