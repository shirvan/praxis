package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/internal/drivers/iaminstanceprofile"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

type IAMInstanceProfileAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI iaminstanceprofile.IAMInstanceProfileAPI
	apiFactory        func(aws.Config) iaminstanceprofile.IAMInstanceProfileAPI
}

func NewIAMInstanceProfileAdapter() *IAMInstanceProfileAdapter {
	return NewIAMInstanceProfileAdapterWithRegistry(auth.LoadFromEnv())
}

func NewIAMInstanceProfileAdapterWithRegistry(accounts *auth.Registry) *IAMInstanceProfileAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &IAMInstanceProfileAdapter{
		auth: accounts,
		apiFactory: func(cfg aws.Config) iaminstanceprofile.IAMInstanceProfileAPI {
			return iaminstanceprofile.NewIAMInstanceProfileAPI(awsclient.NewIAMClient(cfg))
		},
	}
}

func NewIAMInstanceProfileAdapterWithAPI(api iaminstanceprofile.IAMInstanceProfileAPI) *IAMInstanceProfileAdapter {
	return &IAMInstanceProfileAdapter{staticPlanningAPI: api}
}

func (a *IAMInstanceProfileAdapter) Kind() string {
	return iaminstanceprofile.ServiceName
}

func (a *IAMInstanceProfileAdapter) ServiceName() string {
	return iaminstanceprofile.ServiceName
}

func (a *IAMInstanceProfileAdapter) Scope() KeyScope {
	return KeyScopeGlobal
}

func (a *IAMInstanceProfileAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("instance profile name", spec.InstanceProfileName); err != nil {
		return "", err
	}
	return spec.InstanceProfileName, nil
}

func (a *IAMInstanceProfileAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *IAMInstanceProfileAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[iaminstanceprofile.IAMInstanceProfileSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[iaminstanceprofile.IAMInstanceProfileSpec, iaminstanceprofile.IAMInstanceProfileOutputs](
		restate.Object[iaminstanceprofile.IAMInstanceProfileOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[iaminstanceprofile.IAMInstanceProfileOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *IAMInstanceProfileAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *IAMInstanceProfileAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[iaminstanceprofile.IAMInstanceProfileOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"arn":                 out.Arn,
		"instanceProfileId":   out.InstanceProfileId,
		"instanceProfileName": out.InstanceProfileName,
	}, nil
}

func (a *IAMInstanceProfileAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[iaminstanceprofile.IAMInstanceProfileSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	planningAPI, err := a.planningAPI(account)
	if err != nil {
		return "", nil, err
	}

	type describePlanResult struct {
		State iaminstanceprofile.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeInstanceProfile(runCtx, desired.InstanceProfileName)
		if descErr != nil {
			if iaminstanceprofile.IsNotFound(descErr) {
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

	rawDiffs := iaminstanceprofile.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}

	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *IAMInstanceProfileAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return resourceID, nil
}

func (a *IAMInstanceProfileAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, iaminstanceprofile.IAMInstanceProfileOutputs](
		restate.Object[iaminstanceprofile.IAMInstanceProfileOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *IAMInstanceProfileAdapter) decodeSpec(doc resourceDocument) (iaminstanceprofile.IAMInstanceProfileSpec, error) {
	var spec struct {
		Path     string            `json:"path"`
		RoleName string            `json:"roleName"`
		Tags     map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return iaminstanceprofile.IAMInstanceProfileSpec{}, fmt.Errorf("decode IAMInstanceProfile spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return iaminstanceprofile.IAMInstanceProfileSpec{}, fmt.Errorf("IAMInstanceProfile metadata.name is required")
	}
	if strings.TrimSpace(spec.RoleName) == "" {
		return iaminstanceprofile.IAMInstanceProfileSpec{}, fmt.Errorf("IAMInstanceProfile spec.roleName is required")
	}
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return iaminstanceprofile.IAMInstanceProfileSpec{
		Account:             "",
		Path:                spec.Path,
		InstanceProfileName: name,
		RoleName:            spec.RoleName,
		Tags:                spec.Tags,
	}, nil
}

func (a *IAMInstanceProfileAdapter) planningAPI(account string) (iaminstanceprofile.IAMInstanceProfileAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("IAMInstanceProfile adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAMInstanceProfile planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
