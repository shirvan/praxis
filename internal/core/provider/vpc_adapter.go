package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/internal/drivers/vpc"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

// VPCAdapter adapts generic resource documents to the strongly typed VPC driver.
type VPCAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI vpc.VPCAPI
	apiFactory        func(aws.Config) vpc.VPCAPI
}

func NewVPCAdapter() *VPCAdapter {
	return NewVPCAdapterWithRegistry(auth.LoadFromEnv())
}

func NewVPCAdapterWithRegistry(accounts *auth.Registry) *VPCAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &VPCAdapter{
		auth: accounts,
		apiFactory: func(cfg aws.Config) vpc.VPCAPI {
			return vpc.NewVPCAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

func NewVPCAdapterWithAPI(api vpc.VPCAPI) *VPCAdapter {
	return &VPCAdapter{staticPlanningAPI: api}
}

func (a *VPCAdapter) Kind() string {
	return vpc.ServiceName
}

func (a *VPCAdapter) ServiceName() string {
	return vpc.ServiceName
}

func (a *VPCAdapter) Scope() KeyScope {
	return KeyScopeRegion
}

func (a *VPCAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	name := strings.TrimSpace(doc.Metadata.Name)
	if err := ValidateKeyPart("VPC name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

func (a *VPCAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *VPCAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[vpc.VPCSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[vpc.VPCSpec, vpc.VPCOutputs](
		restate.Object[vpc.VPCOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[vpc.VPCOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *VPCAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})

	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *VPCAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[vpc.VPCOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"vpcId":              out.VpcId,
		"cidrBlock":          out.CidrBlock,
		"state":              out.State,
		"enableDnsHostnames": out.EnableDnsHostnames,
		"enableDnsSupport":   out.EnableDnsSupport,
		"instanceTenancy":    out.InstanceTenancy,
		"ownerId":            out.OwnerId,
		"dhcpOptionsId":      out.DhcpOptionsId,
		"isDefault":          out.IsDefault,
	}
	if out.ARN != "" {
		result["arn"] = out.ARN
	}
	return result, nil
}

func (a *VPCAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[vpc.VPCSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[vpc.VPCOutputs](ctx, a.ServiceName(), key, "GetOutputs").
		Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("VPC Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.VpcId == "" {
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
		State vpc.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeVpc(runCtx, outputs.VpcId)
		if descErr != nil {
			if vpc.IsNotFound(descErr) {
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

	rawDiffs := vpc.ComputeFieldDiffs(desired, result.State)
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

func (a *VPCAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *VPCAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, vpc.VPCOutputs](
		restate.Object[vpc.VPCOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *VPCAdapter) decodeSpec(doc resourceDocument) (vpc.VPCSpec, error) {
	var spec vpc.VPCSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return vpc.VPCSpec{}, fmt.Errorf("decode VPC spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return vpc.VPCSpec{}, fmt.Errorf("VPC metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return vpc.VPCSpec{}, fmt.Errorf("VPC spec.region is required")
	}
	if strings.TrimSpace(spec.CidrBlock) == "" {
		return vpc.VPCSpec{}, fmt.Errorf("VPC spec.cidrBlock is required")
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	if spec.Tags["Name"] == "" {
		spec.Tags["Name"] = name
	}
	if spec.InstanceTenancy == "" {
		spec.InstanceTenancy = "default"
	}
	spec.Account = ""
	return spec, nil
}

func (a *VPCAdapter) planningAPI(account string) (vpc.VPCAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("VPC adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve VPC planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
