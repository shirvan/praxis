package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/iamuser"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type IAMUserAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI iamuser.IAMUserAPI
	apiFactory        func(aws.Config) iamuser.IAMUserAPI
}

func NewIAMUserAdapterWithAuth(auth authservice.AuthClient) *IAMUserAdapter {
	return &IAMUserAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) iamuser.IAMUserAPI {
			return iamuser.NewIAMUserAPI(awsclient.NewIAMClient(cfg))
		},
	}
}

func NewIAMUserAdapterWithAPI(api iamuser.IAMUserAPI) *IAMUserAdapter {
	return &IAMUserAdapter{staticPlanningAPI: api}
}

func (a *IAMUserAdapter) Kind() string {
	return iamuser.ServiceName
}

func (a *IAMUserAdapter) ServiceName() string {
	return iamuser.ServiceName
}

func (a *IAMUserAdapter) Scope() KeyScope {
	return KeyScopeGlobal
}

func (a *IAMUserAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("user name", spec.UserName); err != nil {
		return "", err
	}
	return spec.UserName, nil
}

func (a *IAMUserAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *IAMUserAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[iamuser.IAMUserSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[iamuser.IAMUserSpec, iamuser.IAMUserOutputs](
		restate.Object[iamuser.IAMUserOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[iamuser.IAMUserOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *IAMUserAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *IAMUserAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[iamuser.IAMUserOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"arn": out.Arn, "userId": out.UserId, "userName": out.UserName}, nil
}

func (a *IAMUserAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[iamuser.IAMUserSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}

	type describePlanResult struct {
		State iamuser.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeUser(runCtx, desired.UserName)
		if descErr != nil {
			if iamuser.IsNotFound(descErr) {
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

	rawDiffs := iamuser.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *IAMUserAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return resourceID, nil
}

func (a *IAMUserAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, iamuser.IAMUserOutputs](
		restate.Object[iamuser.IAMUserOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *IAMUserAdapter) decodeSpec(doc resourceDocument) (iamuser.IAMUserSpec, error) {
	var spec struct {
		Path                string            `json:"path"`
		PermissionsBoundary string            `json:"permissionsBoundary"`
		InlinePolicies      map[string]string `json:"inlinePolicies"`
		ManagedPolicyArns   []string          `json:"managedPolicyArns"`
		Groups              []string          `json:"groups"`
		Tags                map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return iamuser.IAMUserSpec{}, fmt.Errorf("decode IAMUser spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return iamuser.IAMUserSpec{}, fmt.Errorf("IAMUser metadata.name is required")
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
	if spec.Groups == nil {
		spec.Groups = []string{}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return iamuser.IAMUserSpec{
		Path:                spec.Path,
		UserName:            name,
		PermissionsBoundary: spec.PermissionsBoundary,
		InlinePolicies:      spec.InlinePolicies,
		ManagedPolicyArns:   spec.ManagedPolicyArns,
		Groups:              spec.Groups,
		Tags:                spec.Tags,
	}, nil
}

func (a *IAMUserAdapter) planningAPI(ctx restate.Context, account string) (iamuser.IAMUserAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("IAMUser adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAMUser planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
