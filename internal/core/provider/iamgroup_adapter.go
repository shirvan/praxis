package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/internal/drivers/iamgroup"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

type IAMGroupAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI iamgroup.IAMGroupAPI
	apiFactory        func(aws.Config) iamgroup.IAMGroupAPI
}

func NewIAMGroupAdapter() *IAMGroupAdapter {
	return NewIAMGroupAdapterWithRegistry(auth.LoadFromEnv())
}

func NewIAMGroupAdapterWithRegistry(accounts *auth.Registry) *IAMGroupAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &IAMGroupAdapter{
		auth: accounts,
		apiFactory: func(cfg aws.Config) iamgroup.IAMGroupAPI {
			return iamgroup.NewIAMGroupAPI(awsclient.NewIAMClient(cfg))
		},
	}
}

func NewIAMGroupAdapterWithAPI(api iamgroup.IAMGroupAPI) *IAMGroupAdapter {
	return &IAMGroupAdapter{staticPlanningAPI: api}
}

func (a *IAMGroupAdapter) Kind() string {
	return iamgroup.ServiceName
}

func (a *IAMGroupAdapter) ServiceName() string {
	return iamgroup.ServiceName
}

func (a *IAMGroupAdapter) Scope() KeyScope {
	return KeyScopeGlobal
}

func (a *IAMGroupAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("group name", spec.GroupName); err != nil {
		return "", err
	}
	return spec.GroupName, nil
}

func (a *IAMGroupAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *IAMGroupAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[iamgroup.IAMGroupSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[iamgroup.IAMGroupSpec, iamgroup.IAMGroupOutputs](
		restate.Object[iamgroup.IAMGroupOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[iamgroup.IAMGroupOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *IAMGroupAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *IAMGroupAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[iamgroup.IAMGroupOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"arn": out.Arn, "groupId": out.GroupId, "groupName": out.GroupName}, nil
}

func (a *IAMGroupAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[iamgroup.IAMGroupSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	planningAPI, err := a.planningAPI(account)
	if err != nil {
		return "", nil, err
	}

	type describePlanResult struct {
		State iamgroup.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeGroup(runCtx, desired.GroupName)
		if descErr != nil {
			if iamgroup.IsNotFound(descErr) {
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

	rawDiffs := iamgroup.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *IAMGroupAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return resourceID, nil
}

func (a *IAMGroupAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, iamgroup.IAMGroupOutputs](
		restate.Object[iamgroup.IAMGroupOutputs](ctx, a.ServiceName(), key, "Import"),
	).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *IAMGroupAdapter) decodeSpec(doc resourceDocument) (iamgroup.IAMGroupSpec, error) {
	var spec struct {
		Path              string            `json:"path"`
		InlinePolicies    map[string]string `json:"inlinePolicies"`
		ManagedPolicyArns []string          `json:"managedPolicyArns"`
	}
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return iamgroup.IAMGroupSpec{}, fmt.Errorf("decode IAMGroup spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return iamgroup.IAMGroupSpec{}, fmt.Errorf("IAMGroup metadata.name is required")
	}
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.InlinePolicies == nil {
		spec.InlinePolicies = map[string]string{}
	}
	if spec.ManagedPolicyArns == nil {
		spec.ManagedPolicyArns = []string{}
	}
	return iamgroup.IAMGroupSpec{Path: spec.Path, GroupName: name, InlinePolicies: spec.InlinePolicies, ManagedPolicyArns: spec.ManagedPolicyArns}, nil
}

func (a *IAMGroupAdapter) planningAPI(account string) (iamgroup.IAMGroupAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("IAMGroup adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAMGroup planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
