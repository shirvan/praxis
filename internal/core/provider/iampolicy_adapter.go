package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/drivers/iampolicy"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type IAMPolicyAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI iampolicy.IAMPolicyAPI
	apiFactory        func(aws.Config) iampolicy.IAMPolicyAPI
}

func NewIAMPolicyAdapter() *IAMPolicyAdapter {
	return NewIAMPolicyAdapterWithRegistry(auth.LoadFromEnv())
}

func NewIAMPolicyAdapterWithRegistry(accounts *auth.Registry) *IAMPolicyAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &IAMPolicyAdapter{
		auth: accounts,
		apiFactory: func(cfg aws.Config) iampolicy.IAMPolicyAPI {
			return iampolicy.NewIAMPolicyAPI(awsclient.NewIAMClient(cfg))
		},
	}
}

func NewIAMPolicyAdapterWithAPI(api iampolicy.IAMPolicyAPI) *IAMPolicyAdapter {
	return &IAMPolicyAdapter{staticPlanningAPI: api}
}

func (a *IAMPolicyAdapter) Kind() string {
	return iampolicy.ServiceName
}

func (a *IAMPolicyAdapter) ServiceName() string {
	return iampolicy.ServiceName
}

func (a *IAMPolicyAdapter) Scope() KeyScope {
	return KeyScopeGlobal
}

func (a *IAMPolicyAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("policy name", spec.PolicyName); err != nil {
		return "", err
	}
	return spec.PolicyName, nil
}

func (a *IAMPolicyAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *IAMPolicyAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[iampolicy.IAMPolicySpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[iampolicy.IAMPolicySpec, iampolicy.IAMPolicyOutputs](
		restate.Object[iampolicy.IAMPolicyOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[iampolicy.IAMPolicyOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *IAMPolicyAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *IAMPolicyAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[iampolicy.IAMPolicyOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"arn": out.Arn, "policyId": out.PolicyId, "policyName": out.PolicyName}, nil
}

func (a *IAMPolicyAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[iampolicy.IAMPolicySpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	planningAPI, err := a.planningAPI(account)
	if err != nil {
		return "", nil, err
	}

	type describePlanResult struct {
		State iampolicy.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribePolicyByName(runCtx, desired.PolicyName, desired.Path)
		if descErr != nil {
			if iampolicy.IsNotFound(descErr) {
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

	rawDiffs := iampolicy.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *IAMPolicyAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return resourceID, nil
}

func (a *IAMPolicyAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, iampolicy.IAMPolicyOutputs](
		restate.Object[iampolicy.IAMPolicyOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *IAMPolicyAdapter) decodeSpec(doc resourceDocument) (iampolicy.IAMPolicySpec, error) {
	var spec struct {
		Path           string            `json:"path"`
		PolicyDocument string            `json:"policyDocument"`
		Description    string            `json:"description"`
		Tags           map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return iampolicy.IAMPolicySpec{}, fmt.Errorf("decode IAMPolicy spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return iampolicy.IAMPolicySpec{}, fmt.Errorf("IAMPolicy metadata.name is required")
	}
	if strings.TrimSpace(spec.PolicyDocument) == "" {
		return iampolicy.IAMPolicySpec{}, fmt.Errorf("IAMPolicy spec.policyDocument is required")
	}
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return iampolicy.IAMPolicySpec{Path: spec.Path, PolicyName: name, PolicyDocument: spec.PolicyDocument, Description: spec.Description, Tags: spec.Tags}, nil
}

func (a *IAMPolicyAdapter) planningAPI(account string) (iampolicy.IAMPolicyAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("IAMPolicy adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAMPolicy planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
