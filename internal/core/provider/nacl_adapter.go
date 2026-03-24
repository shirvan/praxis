package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/nacl"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type NetworkACLAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI nacl.NetworkACLAPI
	apiFactory        func(aws.Config) nacl.NetworkACLAPI
}

func NewNetworkACLAdapterWithAuth(auth authservice.AuthClient) *NetworkACLAdapter {
	return &NetworkACLAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) nacl.NetworkACLAPI {
			return nacl.NewNetworkACLAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

func NewNetworkACLAdapterWithAPI(api nacl.NetworkACLAPI) *NetworkACLAdapter {
	return &NetworkACLAdapter{staticPlanningAPI: api}
}

func (a *NetworkACLAdapter) Kind() string {
	return nacl.ServiceName
}

func (a *NetworkACLAdapter) ServiceName() string {
	return nacl.ServiceName
}

func (a *NetworkACLAdapter) Scope() KeyScope {
	return KeyScopeCustom
}

func (a *NetworkACLAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	name := strings.TrimSpace(doc.Metadata.Name)
	if err := ValidateKeyPart("network ACL name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.VpcId, name), nil
}

func (a *NetworkACLAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *NetworkACLAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[nacl.NetworkACLSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[nacl.NetworkACLSpec, nacl.NetworkACLOutputs](
		restate.Object[nacl.NetworkACLOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[nacl.NetworkACLOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *NetworkACLAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *NetworkACLAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[nacl.NetworkACLOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"networkAclId": out.NetworkAclId,
		"vpcId":        out.VpcId,
		"isDefault":    out.IsDefault,
		"ingressRules": out.IngressRules,
		"egressRules":  out.EgressRules,
		"associations": out.Associations,
	}, nil
}

func (a *NetworkACLAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[nacl.NetworkACLSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[nacl.NetworkACLOutputs](ctx, a.ServiceName(), key, "GetOutputs").
		Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("NetworkACL Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.NetworkAclId == "" {
		fields, fieldErr := createFieldDiffsFromSpec(desired)
		if fieldErr != nil {
			return "", nil, fieldErr
		}
		return types.OpCreate, fields, nil
	}

	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}

	type describePlanResult struct {
		State nacl.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeNetworkACL(runCtx, outputs.NetworkAclId)
		if descErr != nil {
			if nacl.IsNotFound(descErr) {
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

	rawDiffs := nacl.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}

	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *NetworkACLAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *NetworkACLAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, nacl.NetworkACLOutputs](
		restate.Object[nacl.NetworkACLOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *NetworkACLAdapter) decodeSpec(doc resourceDocument) (nacl.NetworkACLSpec, error) {
	var spec nacl.NetworkACLSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return nacl.NetworkACLSpec{}, fmt.Errorf("decode NetworkACL spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return nacl.NetworkACLSpec{}, fmt.Errorf("NetworkACL metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return nacl.NetworkACLSpec{}, fmt.Errorf("NetworkACL spec.region is required")
	}
	if strings.TrimSpace(spec.VpcId) == "" {
		return nacl.NetworkACLSpec{}, fmt.Errorf("NetworkACL spec.vpcId is required")
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	if spec.Tags["Name"] == "" {
		spec.Tags["Name"] = name
	}
	spec.Account = ""
	return spec, nil
}

func (a *NetworkACLAdapter) planningAPI(ctx restate.Context, account string) (nacl.NetworkACLAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("NetworkACL adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve NetworkACL planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
