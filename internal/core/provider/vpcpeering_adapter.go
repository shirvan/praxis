package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/vpcpeering"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type VPCPeeringAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI vpcpeering.VPCPeeringAPI
	apiFactory        func(aws.Config) vpcpeering.VPCPeeringAPI
}

func NewVPCPeeringAdapterWithAuth(auth authservice.AuthClient) *VPCPeeringAdapter {
	return &VPCPeeringAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) vpcpeering.VPCPeeringAPI {
			return vpcpeering.NewVPCPeeringAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

func NewVPCPeeringAdapterWithAPI(api vpcpeering.VPCPeeringAPI) *VPCPeeringAdapter {
	return &VPCPeeringAdapter{staticPlanningAPI: api}
}

func (a *VPCPeeringAdapter) Kind() string {
	return vpcpeering.ServiceName
}

func (a *VPCPeeringAdapter) ServiceName() string {
	return vpcpeering.ServiceName
}

func (a *VPCPeeringAdapter) Scope() KeyScope {
	return KeyScopeRegion
}

func (a *VPCPeeringAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("VPC peering connection name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

func (a *VPCPeeringAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *VPCPeeringAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[vpcpeering.VPCPeeringSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[vpcpeering.VPCPeeringSpec, vpcpeering.VPCPeeringOutputs](
		restate.Object[vpcpeering.VPCPeeringOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[vpcpeering.VPCPeeringOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *VPCPeeringAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *VPCPeeringAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[vpcpeering.VPCPeeringOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"vpcPeeringConnectionId": out.VpcPeeringConnectionId,
		"requesterVpcId":         out.RequesterVpcId,
		"accepterVpcId":          out.AccepterVpcId,
		"requesterCidrBlock":     out.RequesterCidrBlock,
		"accepterCidrBlock":      out.AccepterCidrBlock,
		"status":                 out.Status,
		"requesterOwnerId":       out.RequesterOwnerId,
		"accepterOwnerId":        out.AccepterOwnerId,
	}, nil
}

func (a *VPCPeeringAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[vpcpeering.VPCPeeringSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[vpcpeering.VPCPeeringOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("VPCPeeringConnection Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.VpcPeeringConnectionId == "" {
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
		State vpcpeering.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeVPCPeeringConnection(runCtx, outputs.VpcPeeringConnectionId)
		if descErr != nil {
			if vpcpeering.IsNotFound(descErr) {
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

	rawDiffs := vpcpeering.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *VPCPeeringAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *VPCPeeringAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, vpcpeering.VPCPeeringOutputs](
		restate.Object[vpcpeering.VPCPeeringOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *VPCPeeringAdapter) decodeSpec(doc resourceDocument) (vpcpeering.VPCPeeringSpec, error) {
	var spec vpcpeering.VPCPeeringSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return vpcpeering.VPCPeeringSpec{}, fmt.Errorf("decode VPCPeeringConnection spec: %w", err)
	}
	var raw struct {
		AutoAccept *bool `json:"autoAccept"`
	}
	if err := json.Unmarshal(doc.Spec, &raw); err != nil {
		return vpcpeering.VPCPeeringSpec{}, fmt.Errorf("decode VPCPeeringConnection defaults: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return vpcpeering.VPCPeeringSpec{}, fmt.Errorf("VPCPeeringConnection metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return vpcpeering.VPCPeeringSpec{}, fmt.Errorf("VPCPeeringConnection spec.region is required")
	}
	if strings.TrimSpace(spec.RequesterVpcId) == "" {
		return vpcpeering.VPCPeeringSpec{}, fmt.Errorf("VPCPeeringConnection spec.requesterVpcId is required")
	}
	if strings.TrimSpace(spec.AccepterVpcId) == "" {
		return vpcpeering.VPCPeeringSpec{}, fmt.Errorf("VPCPeeringConnection spec.accepterVpcId is required")
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	if spec.Tags["Name"] == "" {
		spec.Tags["Name"] = name
	}
	if raw.AutoAccept == nil {
		spec.AutoAccept = true
	}
	spec.Account = ""
	return spec, nil
}

func (a *VPCPeeringAdapter) planningAPI(ctx restate.Context, account string) (vpcpeering.VPCPeeringAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("VPCPeeringConnection adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve VPCPeeringConnection planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
