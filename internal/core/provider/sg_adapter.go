package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/sg"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func (a *SecurityGroupAdapter) Scope() KeyScope {
	return KeyScopeCustom
}

// SecurityGroupAdapter adapts generic resource documents to the strongly typed
// EC2 security group driver.
type SecurityGroupAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI sg.SGAPI
	apiFactory        func(aws.Config) sg.SGAPI
}

func NewSecurityGroupAdapterWithAuth(auth authservice.AuthClient) *SecurityGroupAdapter {
	return &SecurityGroupAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) sg.SGAPI {
			return sg.NewSGAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

// NewSecurityGroupAdapterWithAPI injects a fixed EC2 planning API. This is primarily
// useful in tests that do not need per-account planning behavior.
func NewSecurityGroupAdapterWithAPI(api sg.SGAPI) *SecurityGroupAdapter {
	return &SecurityGroupAdapter{staticPlanningAPI: api}
}

func (a *SecurityGroupAdapter) Kind() string {
	return sg.ServiceName
}

func (a *SecurityGroupAdapter) ServiceName() string {
	return sg.ServiceName
}

func (a *SecurityGroupAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("VPC ID", spec.VpcId); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("group name", spec.GroupName); err != nil {
		return "", err
	}
	return JoinKey(spec.VpcId, spec.GroupName), nil
}

func (a *SecurityGroupAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *SecurityGroupAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[sg.SecurityGroupSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[sg.SecurityGroupSpec, sg.SecurityGroupOutputs](
		restate.Object[sg.SecurityGroupOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[sg.SecurityGroupOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *SecurityGroupAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})

	return &deleteHandle{
		id:  fut.GetInvocationId(),
		raw: fut,
	}, nil
}

func (a *SecurityGroupAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[sg.SecurityGroupOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"groupId":  out.GroupId,
		"groupArn": out.GroupArn,
		"vpcId":    out.VpcId,
	}, nil
}

func (a *SecurityGroupAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[sg.SecurityGroupSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}

	// describePlanResult packages the describe response so that "not found" is
	// a successful journal entry rather than a retried error.
	type describePlanResult struct {
		State sg.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		out, descErr := planningAPI.FindSecurityGroup(runCtx, desired.GroupName, desired.VpcId)
		if descErr != nil {
			if sg.IsNotFound(descErr) {
				return describePlanResult{Found: false}, nil
			}
			return describePlanResult{}, restate.TerminalError(descErr, 500)
		}
		return describePlanResult{State: out, Found: true}, nil
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
	observed := result.State

	rawDiffs := sg.ComputeFieldDiffs(desired, observed)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}

	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{
			Path:     diff.Path,
			OldValue: diff.OldValue,
			NewValue: diff.NewValue,
		})
	}
	return types.OpUpdate, fields, nil
}

func (a *SecurityGroupAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return resourceID, nil
}

func (a *SecurityGroupAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, sg.SecurityGroupOutputs](
		restate.Object[sg.SecurityGroupOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *SecurityGroupAdapter) decodeSpec(doc resourceDocument) (sg.SecurityGroupSpec, error) {
	var spec sg.SecurityGroupSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return sg.SecurityGroupSpec{}, fmt.Errorf("decode SecurityGroup spec: %w", err)
	}
	if strings.TrimSpace(spec.GroupName) == "" {
		return sg.SecurityGroupSpec{}, fmt.Errorf("SecurityGroup spec.groupName is required")
	}
	spec.Account = ""
	return spec, nil
}

func (a *SecurityGroupAdapter) planningAPI(ctx restate.Context, account string) (sg.SGAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("SecurityGroup adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve SecurityGroup planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
