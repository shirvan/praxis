package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/ami"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type AMIAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI ami.AMIAPI
	apiFactory        func(aws.Config) ami.AMIAPI
}

func NewAMIAdapterWithAuth(auth authservice.AuthClient) *AMIAdapter {
	return &AMIAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) ami.AMIAPI {
			return ami.NewAMIAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

func NewAMIAdapterWithAPI(api ami.AMIAPI) *AMIAdapter {
	return &AMIAdapter{staticPlanningAPI: api}
}

func (a *AMIAdapter) Kind() string {
	return ami.ServiceName
}

func (a *AMIAdapter) ServiceName() string {
	return ami.ServiceName
}

func (a *AMIAdapter) Scope() KeyScope {
	return KeyScopeRegion
}

func (a *AMIAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("AMI name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

func (a *AMIAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *AMIAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[ami.AMISpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[ami.AMISpec, ami.AMIOutputs](
		restate.Object[ami.AMIOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[ami.AMIOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *AMIAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *AMIAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[ami.AMIOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"imageId":            out.ImageId,
		"name":               out.Name,
		"state":              out.State,
		"architecture":       out.Architecture,
		"virtualizationType": out.VirtualizationType,
		"rootDeviceName":     out.RootDeviceName,
		"ownerId":            out.OwnerId,
		"creationDate":       out.CreationDate,
	}, nil
}

func (a *AMIAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[ami.AMISpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[ami.AMIOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("AMI Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.ImageId == "" {
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
		State ami.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeImage(runCtx, outputs.ImageId)
		if descErr != nil {
			if ami.IsNotFound(descErr) {
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

	rawDiffs := ami.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}

	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *AMIAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	if looksLikeAMIID(resourceID) {
		if a.staticPlanningAPI == nil {
			return "", fmt.Errorf("AMI adapter planning API is not configured for import key resolution")
		}
		obs, err := a.staticPlanningAPI.DescribeImage(context.Background(), resourceID)
		if err != nil {
			return "", fmt.Errorf("resolve AMI import key for %q: %w", resourceID, err)
		}
		if strings.TrimSpace(obs.Name) != "" {
			return JoinKey(region, obs.Name), nil
		}
	}
	return JoinKey(region, resourceID), nil
}

func (a *AMIAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, ami.AMIOutputs](
		restate.Object[ami.AMIOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *AMIAdapter) decodeSpec(doc resourceDocument) (ami.AMISpec, error) {
	var spec ami.AMISpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return ami.AMISpec{}, fmt.Errorf("decode AMI spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return ami.AMISpec{}, fmt.Errorf("AMI metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return ami.AMISpec{}, fmt.Errorf("AMI spec.region is required")
	}
	spec.Name = name
	if err := amiValidateSource(spec.Source); err != nil {
		return ami.AMISpec{}, err
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

func (a *AMIAdapter) planningAPI(ctx restate.Context, account string) (ami.AMIAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("AMI adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve AMI planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}

func amiValidateSource(source ami.SourceSpec) error {
	hasSnapshot := source.FromSnapshot != nil
	hasAMI := source.FromAMI != nil
	if !hasSnapshot && !hasAMI {
		return fmt.Errorf("exactly one of source.fromSnapshot or source.fromAMI must be specified")
	}
	if hasSnapshot && hasAMI {
		return fmt.Errorf("cannot specify both source.fromSnapshot and source.fromAMI")
	}
	if hasSnapshot && strings.TrimSpace(source.FromSnapshot.SnapshotId) == "" {
		return fmt.Errorf("AMI spec.source.fromSnapshot.snapshotId is required")
	}
	if hasAMI && strings.TrimSpace(source.FromAMI.SourceImageId) == "" {
		return fmt.Errorf("AMI spec.source.fromAMI.sourceImageId is required")
	}
	return nil
}

func looksLikeAMIID(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.HasPrefix(value, "ami-")
}
