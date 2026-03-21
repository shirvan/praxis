package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/internal/drivers/ebs"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

type EBSAdapter struct {
	auth              *auth.Registry
	staticPlanningAPI ebs.EBSAPI
	apiFactory        func(aws.Config) ebs.EBSAPI
}

func NewEBSAdapter() *EBSAdapter {
	return NewEBSAdapterWithRegistry(auth.LoadFromEnv())
}

func NewEBSAdapterWithRegistry(accounts *auth.Registry) *EBSAdapter {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	return &EBSAdapter{
		auth: accounts,
		apiFactory: func(cfg aws.Config) ebs.EBSAPI {
			return ebs.NewEBSAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

func NewEBSAdapterWithAPI(api ebs.EBSAPI) *EBSAdapter {
	return &EBSAdapter{staticPlanningAPI: api}
}

func (a *EBSAdapter) Kind() string {
	return ebs.ServiceName
}

func (a *EBSAdapter) ServiceName() string {
	return ebs.ServiceName
}

func (a *EBSAdapter) Scope() KeyScope {
	return KeyScopeRegion
}

func (a *EBSAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("volume name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

func (a *EBSAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *EBSAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[ebs.EBSVolumeSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[ebs.EBSVolumeSpec, ebs.EBSVolumeOutputs](
		restate.Object[ebs.EBSVolumeOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[ebs.EBSVolumeOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *EBSAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})

	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *EBSAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[ebs.EBSVolumeOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"volumeId":         out.VolumeId,
		"availabilityZone": out.AvailabilityZone,
		"state":            out.State,
		"sizeGiB":          out.SizeGiB,
		"volumeType":       out.VolumeType,
		"encrypted":        out.Encrypted,
	}
	if out.ARN != "" {
		result["arn"] = out.ARN
	}
	return result, nil
}

func (a *EBSAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[ebs.EBSVolumeSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[ebs.EBSVolumeOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("EBS Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.VolumeId == "" {
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
		State ebs.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeVolume(runCtx, outputs.VolumeId)
		if descErr != nil {
			if ebs.IsNotFound(descErr) {
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

	rawDiffs := ebs.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}

	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *EBSAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

func (a *EBSAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, ebs.EBSVolumeOutputs](
		restate.Object[ebs.EBSVolumeOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *EBSAdapter) decodeSpec(doc resourceDocument) (ebs.EBSVolumeSpec, error) {
	var spec ebs.EBSVolumeSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return ebs.EBSVolumeSpec{}, fmt.Errorf("decode EBSVolume spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return ebs.EBSVolumeSpec{}, fmt.Errorf("EBSVolume metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return ebs.EBSVolumeSpec{}, fmt.Errorf("EBSVolume spec.region is required")
	}
	if strings.TrimSpace(spec.AvailabilityZone) == "" {
		return ebs.EBSVolumeSpec{}, fmt.Errorf("EBSVolume spec.availabilityZone is required")
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	if spec.Tags["Name"] == "" {
		spec.Tags["Name"] = name
	}
	if spec.VolumeType == "" {
		spec.VolumeType = "gp3"
	}
	if spec.SizeGiB == 0 {
		spec.SizeGiB = 20
	}
	spec.Account = ""
	return spec, nil
}

func (a *EBSAdapter) planningAPI(account string) (ebs.EBSAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("EBS adapter planning API is not configured")
	}
	awsCfg, err := a.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve EBS planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
