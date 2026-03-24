package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/subnet"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type SubnetAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI subnet.SubnetAPI
	apiFactory        func(aws.Config) subnet.SubnetAPI
}

func NewSubnetAdapterWithAuth(auth authservice.AuthClient) *SubnetAdapter {
	return &SubnetAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) subnet.SubnetAPI {
			return subnet.NewSubnetAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

func NewSubnetAdapterWithAPI(api subnet.SubnetAPI) *SubnetAdapter {
	return &SubnetAdapter{staticPlanningAPI: api}
}

func (a *SubnetAdapter) Kind() string {
	return subnet.ServiceName
}

func (a *SubnetAdapter) ServiceName() string {
	return subnet.ServiceName
}

func (a *SubnetAdapter) Scope() KeyScope {
	return KeyScopeCustom
}

func (a *SubnetAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("subnet name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.VpcId, name), nil
}

func (a *SubnetAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *SubnetAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[subnet.SubnetSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[subnet.SubnetSpec, subnet.SubnetOutputs](
		restate.Object[subnet.SubnetOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[subnet.SubnetOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *SubnetAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})

	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *SubnetAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[subnet.SubnetOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"subnetId":            out.SubnetId,
		"vpcId":               out.VpcId,
		"cidrBlock":           out.CidrBlock,
		"availabilityZone":    out.AvailabilityZone,
		"availabilityZoneId":  out.AvailabilityZoneId,
		"mapPublicIpOnLaunch": out.MapPublicIpOnLaunch,
		"state":               out.State,
		"ownerId":             out.OwnerId,
		"availableIpCount":    out.AvailableIpCount,
	}
	if out.ARN != "" {
		result["arn"] = out.ARN
	}
	return result, nil
}

func (a *SubnetAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[subnet.SubnetSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[subnet.SubnetOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("Subnet Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.SubnetId == "" {
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
		State subnet.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeSubnet(runCtx, outputs.SubnetId)
		if descErr != nil {
			if subnet.IsNotFound(descErr) {
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

	rawDiffs := subnet.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}

	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *SubnetAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *SubnetAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, subnet.SubnetOutputs](
		restate.Object[subnet.SubnetOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *SubnetAdapter) Lookup(ctx restate.Context, account string, filter LookupFilter) (map[string]any, error) {
	api, err := a.lookupAPI(ctx, account, filter.Region)
	if err != nil {
		return nil, restate.TerminalError(err, 500)
	}
	observed, err := restate.Run(ctx, func(runCtx restate.RunContext) (subnet.ObservedState, error) {
		return lookupSubnet(runCtx, api, filter)
	})
	if err != nil {
		return nil, classifyLookupError(err, subnet.IsNotFound)
	}
	if !matchesSubnetFilter(observed, filter) {
		return nil, restate.TerminalError(fmt.Errorf("data source lookup: no Subnet found matching filter"), 404)
	}
	outputs, err := a.NormalizeOutputs(subnet.SubnetOutputs{
		SubnetId:            observed.SubnetId,
		ARN:                 subnetARN(filter.Region, observed.OwnerId, observed.SubnetId),
		VpcId:               observed.VpcId,
		CidrBlock:           observed.CidrBlock,
		AvailabilityZone:    observed.AvailabilityZone,
		AvailabilityZoneId:  observed.AvailabilityZoneId,
		MapPublicIpOnLaunch: observed.MapPublicIpOnLaunch,
		State:               observed.State,
		OwnerId:             observed.OwnerId,
		AvailableIpCount:    observed.AvailableIpCount,
	})
	if err != nil {
		return nil, restate.TerminalError(err, 500)
	}
	return outputs, nil
}

func (a *SubnetAdapter) decodeSpec(doc resourceDocument) (subnet.SubnetSpec, error) {
	var spec subnet.SubnetSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return subnet.SubnetSpec{}, fmt.Errorf("decode Subnet spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return subnet.SubnetSpec{}, fmt.Errorf("Subnet metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return subnet.SubnetSpec{}, fmt.Errorf("Subnet spec.region is required")
	}
	if strings.TrimSpace(spec.VpcId) == "" {
		return subnet.SubnetSpec{}, fmt.Errorf("Subnet spec.vpcId is required")
	}
	if strings.TrimSpace(spec.CidrBlock) == "" {
		return subnet.SubnetSpec{}, fmt.Errorf("Subnet spec.cidrBlock is required")
	}
	if strings.TrimSpace(spec.AvailabilityZone) == "" {
		return subnet.SubnetSpec{}, fmt.Errorf("Subnet spec.availabilityZone is required")
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

func (a *SubnetAdapter) planningAPI(ctx restate.Context, account string) (subnet.SubnetAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("Subnet adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Subnet planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}

func (a *SubnetAdapter) lookupAPI(ctx restate.Context, account string, region string) (subnet.SubnetAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("Subnet adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Subnet planning account %q: %w", account, err)
	}
	if strings.TrimSpace(region) != "" {
		awsCfg.Region = strings.TrimSpace(region)
	}
	return a.apiFactory(awsCfg), nil
}

func lookupSubnet(ctx restate.RunContext, api subnet.SubnetAPI, filter LookupFilter) (subnet.ObservedState, error) {
	if strings.TrimSpace(filter.ID) != "" {
		return api.DescribeSubnet(ctx, strings.TrimSpace(filter.ID))
	}
	tags := lookupTags(filter)
	if len(tags) == 0 {
		return subnet.ObservedState{}, fmt.Errorf("Subnet lookup requires at least one of: id, name, tag")
	}
	id, err := api.FindByTags(ctx, tags)
	if err != nil {
		return subnet.ObservedState{}, err
	}
	if strings.TrimSpace(id) == "" {
		return subnet.ObservedState{}, fmt.Errorf("not found")
	}
	return api.DescribeSubnet(ctx, id)
}

func matchesSubnetFilter(observed subnet.ObservedState, filter LookupFilter) bool {
	if strings.TrimSpace(filter.ID) != "" && observed.SubnetId != strings.TrimSpace(filter.ID) {
		return false
	}
	if strings.TrimSpace(filter.Name) != "" && observed.Tags["Name"] != strings.TrimSpace(filter.Name) {
		return false
	}
	for key, value := range filter.Tag {
		if observed.Tags[key] != value {
			return false
		}
	}
	return true
}

func subnetARN(region, ownerID, subnetID string) string {
	if strings.TrimSpace(region) == "" || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(subnetID) == "" {
		return ""
	}
	return fmt.Sprintf("arn:aws:ec2:%s:%s:subnet/%s", region, ownerID, subnetID)
}
