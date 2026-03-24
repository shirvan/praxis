package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/eip"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type EIPAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI eip.EIPAPI
	apiFactory        func(aws.Config) eip.EIPAPI
}

func NewEIPAdapterWithAuth(auth authservice.AuthClient) *EIPAdapter {
	return &EIPAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) eip.EIPAPI {
			return eip.NewEIPAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

func NewEIPAdapterWithAPI(api eip.EIPAPI) *EIPAdapter {
	return &EIPAdapter{staticPlanningAPI: api}
}

func (a *EIPAdapter) Kind() string {
	return eip.ServiceName
}

func (a *EIPAdapter) ServiceName() string {
	return eip.ServiceName
}

func (a *EIPAdapter) Scope() KeyScope {
	return KeyScopeRegion
}

func (a *EIPAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("elastic IP name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

func (a *EIPAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *EIPAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[eip.ElasticIPSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[eip.ElasticIPSpec, eip.ElasticIPOutputs](
		restate.Object[eip.ElasticIPOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[eip.ElasticIPOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *EIPAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})

	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *EIPAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[eip.ElasticIPOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"allocationId":       out.AllocationId,
		"publicIp":           out.PublicIp,
		"domain":             out.Domain,
		"networkBorderGroup": out.NetworkBorderGroup,
	}
	if out.ARN != "" {
		result["arn"] = out.ARN
	}
	return result, nil
}

func (a *EIPAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[eip.ElasticIPSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[eip.ElasticIPOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("ElasticIP Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.AllocationId == "" {
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
		State eip.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeAddress(runCtx, outputs.AllocationId)
		if descErr != nil {
			if eip.IsNotFound(descErr) {
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

	rawDiffs := eip.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}

	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *EIPAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *EIPAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, eip.ElasticIPOutputs](
		restate.Object[eip.ElasticIPOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *EIPAdapter) decodeSpec(doc resourceDocument) (eip.ElasticIPSpec, error) {
	var spec eip.ElasticIPSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return eip.ElasticIPSpec{}, fmt.Errorf("decode ElasticIP spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return eip.ElasticIPSpec{}, fmt.Errorf("ElasticIP metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return eip.ElasticIPSpec{}, fmt.Errorf("ElasticIP spec.region is required")
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	if spec.Tags["Name"] == "" {
		spec.Tags["Name"] = name
	}
	if spec.Domain == "" {
		spec.Domain = "vpc"
	}
	if spec.Domain != "vpc" {
		return eip.ElasticIPSpec{}, fmt.Errorf("ElasticIP spec.domain must be \"vpc\"")
	}
	spec.Account = ""
	return spec, nil
}

func (a *EIPAdapter) planningAPI(ctx restate.Context, account string) (eip.EIPAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("ElasticIP adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve ElasticIP planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
