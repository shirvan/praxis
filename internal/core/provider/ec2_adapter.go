package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/ec2"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type EC2Adapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI ec2.EC2API
	apiFactory        func(aws.Config) ec2.EC2API
}

func NewEC2AdapterWithAuth(auth authservice.AuthClient) *EC2Adapter {
	return &EC2Adapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) ec2.EC2API {
			return ec2.NewEC2API(awsclient.NewEC2Client(cfg))
		},
	}
}

func NewEC2AdapterWithAPI(api ec2.EC2API) *EC2Adapter {
	return &EC2Adapter{staticPlanningAPI: api}
}

func (a *EC2Adapter) Kind() string {
	return ec2.ServiceName
}

func (a *EC2Adapter) ServiceName() string {
	return ec2.ServiceName
}

func (a *EC2Adapter) Scope() KeyScope {
	return KeyScopeRegion
}

func (a *EC2Adapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("instance name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

func (a *EC2Adapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *EC2Adapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[ec2.EC2InstanceSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[ec2.EC2InstanceSpec, ec2.EC2InstanceOutputs](
		restate.Object[ec2.EC2InstanceOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[ec2.EC2InstanceOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *EC2Adapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})

	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *EC2Adapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[ec2.EC2InstanceOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"instanceId":       out.InstanceId,
		"privateIpAddress": out.PrivateIpAddress,
		"privateDnsName":   out.PrivateDnsName,
		"state":            out.State,
		"subnetId":         out.SubnetId,
		"vpcId":            out.VpcId,
	}
	if out.ARN != "" {
		result["arn"] = out.ARN
	}
	if out.PublicIpAddress != "" {
		result["publicIpAddress"] = out.PublicIpAddress
	}
	return result, nil
}

func (a *EC2Adapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[ec2.EC2InstanceSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[ec2.EC2InstanceOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("EC2 Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.InstanceId == "" {
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
		State ec2.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeInstance(runCtx, outputs.InstanceId)
		if descErr != nil {
			if ec2.IsNotFound(descErr) {
				return describePlanResult{Found: false}, nil
			}
			return describePlanResult{}, restate.TerminalError(descErr, 500)
		}
		if obs.State == "terminated" || obs.State == "shutting-down" {
			return describePlanResult{Found: false}, nil
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

	rawDiffs := ec2.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}

	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *EC2Adapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *EC2Adapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, ec2.EC2InstanceOutputs](
		restate.Object[ec2.EC2InstanceOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *EC2Adapter) decodeSpec(doc resourceDocument) (ec2.EC2InstanceSpec, error) {
	var spec ec2.EC2InstanceSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return ec2.EC2InstanceSpec{}, fmt.Errorf("decode EC2Instance spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return ec2.EC2InstanceSpec{}, fmt.Errorf("EC2Instance metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return ec2.EC2InstanceSpec{}, fmt.Errorf("EC2Instance spec.region is required")
	}
	if strings.TrimSpace(spec.ImageId) == "" {
		return ec2.EC2InstanceSpec{}, fmt.Errorf("EC2Instance spec.imageId is required")
	}
	if strings.TrimSpace(spec.InstanceType) == "" {
		return ec2.EC2InstanceSpec{}, fmt.Errorf("EC2Instance spec.instanceType is required")
	}
	if strings.TrimSpace(spec.SubnetId) == "" {
		return ec2.EC2InstanceSpec{}, fmt.Errorf("EC2Instance spec.subnetId is required")
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

func (a *EC2Adapter) planningAPI(ctx restate.Context, account string) (ec2.EC2API, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("EC2 adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve EC2 planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
