package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/targetgroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type TargetGroupAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI targetgroup.TargetGroupAPI
	apiFactory        func(aws.Config) targetgroup.TargetGroupAPI
}

func NewTargetGroupAdapterWithAuth(auth authservice.AuthClient) *TargetGroupAdapter {
	return &TargetGroupAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) targetgroup.TargetGroupAPI {
			return targetgroup.NewTargetGroupAPI(awsclient.NewELBv2Client(cfg))
		},
	}
}

func NewTargetGroupAdapterWithAPI(api targetgroup.TargetGroupAPI) *TargetGroupAdapter {
	return &TargetGroupAdapter{staticPlanningAPI: api}
}

func (a *TargetGroupAdapter) Kind() string        { return targetgroup.ServiceName }
func (a *TargetGroupAdapter) ServiceName() string { return targetgroup.ServiceName }
func (a *TargetGroupAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *TargetGroupAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("target group name", spec.Name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.Name), nil
}

func (a *TargetGroupAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *TargetGroupAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[targetgroup.TargetGroupSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	fut := restate.WithRequestType[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs](restate.Object[targetgroup.TargetGroupOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[targetgroup.TargetGroupOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *TargetGroupAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *TargetGroupAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[targetgroup.TargetGroupOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"targetGroupArn": out.TargetGroupArn, "targetGroupName": out.TargetGroupName}, nil
}

func (a *TargetGroupAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[targetgroup.TargetGroupSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[targetgroup.TargetGroupOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("TargetGroup Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}
	identifier := outputs.TargetGroupArn
	if identifier == "" {
		identifier = desired.Name
	}
	type describePlanResult struct {
		State targetgroup.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeTargetGroup(runCtx, identifier)
		if descErr != nil {
			if targetgroup.IsNotFound(descErr) {
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
	rawDiffs := targetgroup.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *TargetGroupAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *TargetGroupAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, targetgroup.TargetGroupOutputs](restate.Object[targetgroup.TargetGroupOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *TargetGroupAdapter) decodeSpec(doc resourceDocument) (targetgroup.TargetGroupSpec, error) {
	var spec targetgroup.TargetGroupSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return targetgroup.TargetGroupSpec{}, fmt.Errorf("decode TargetGroup spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return targetgroup.TargetGroupSpec{}, fmt.Errorf("TargetGroup metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return targetgroup.TargetGroupSpec{}, fmt.Errorf("TargetGroup spec.region is required")
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

func (a *TargetGroupAdapter) planningAPI(ctx restate.Context, account string) (targetgroup.TargetGroupAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("TargetGroup adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve TargetGroup planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
