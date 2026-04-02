package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/snstopic"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type SNSTopicAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI snstopic.TopicAPI
	apiFactory        func(aws.Config) snstopic.TopicAPI
}

func NewSNSTopicAdapterWithAuth(auth authservice.AuthClient) *SNSTopicAdapter {
	return &SNSTopicAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) snstopic.TopicAPI {
			return snstopic.NewTopicAPI(awsclient.NewSNSClient(cfg))
		},
	}
}

func NewSNSTopicAdapterWithAPI(api snstopic.TopicAPI) *SNSTopicAdapter {
	return &SNSTopicAdapter{staticPlanningAPI: api}
}

func (a *SNSTopicAdapter) Kind() string        { return snstopic.ServiceName }
func (a *SNSTopicAdapter) ServiceName() string { return snstopic.ServiceName }
func (a *SNSTopicAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *SNSTopicAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	name := strings.TrimSpace(spec.TopicName)
	if name == "" {
		name = strings.TrimSpace(doc.Metadata.Name)
	}
	if err := ValidateKeyPart("topic name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

func (a *SNSTopicAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *SNSTopicAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[snstopic.SNSTopicSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[snstopic.SNSTopicSpec, snstopic.SNSTopicOutputs](
		restate.Object[snstopic.SNSTopicOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[snstopic.SNSTopicOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *SNSTopicAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *SNSTopicAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[snstopic.SNSTopicOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"topicArn":  out.TopicArn,
		"topicName": out.TopicName,
	}
	if out.Owner != "" {
		result["owner"] = out.Owner
	}
	return result, nil
}

func (a *SNSTopicAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[snstopic.SNSTopicSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[snstopic.SNSTopicOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("SNSTopic Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.TopicArn == "" {
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
		State snstopic.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.GetTopicAttributes(runCtx, outputs.TopicArn)
		if descErr != nil {
			if snstopic.IsNotFound(descErr) {
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

	rawDiffs := snstopic.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *SNSTopicAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	// resourceID may be a topic ARN (arn:aws:sns:<region>:<account>:<topicName>) or just the topic name.
	name := resourceID
	if strings.HasPrefix(resourceID, "arn:aws:sns:") {
		parts := strings.SplitN(resourceID, ":", 6)
		if len(parts) >= 6 {
			name = parts[5]
		}
	}
	if err := ValidateKeyPart("topic name", name); err != nil {
		return "", err
	}
	return JoinKey(region, name), nil
}

func (a *SNSTopicAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, snstopic.SNSTopicOutputs](
		restate.Object[snstopic.SNSTopicOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *SNSTopicAdapter) decodeSpec(doc resourceDocument) (snstopic.SNSTopicSpec, error) {
	var spec snstopic.SNSTopicSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return snstopic.SNSTopicSpec{}, fmt.Errorf("decode SNSTopic spec: %w", err)
	}
	if strings.TrimSpace(spec.Region) == "" {
		return snstopic.SNSTopicSpec{}, fmt.Errorf("SNSTopic spec.region is required")
	}
	name := strings.TrimSpace(spec.TopicName)
	if name == "" {
		name = strings.TrimSpace(doc.Metadata.Name)
	}
	if name == "" {
		return snstopic.SNSTopicSpec{}, fmt.Errorf("SNSTopic spec.topicName or metadata.name is required")
	}
	if spec.TopicName == "" {
		spec.TopicName = name
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	spec.Account = ""
	return spec, nil
}

func (a *SNSTopicAdapter) planningAPI(ctx restate.Context, account string) (snstopic.TopicAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("SNSTopic adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve SNSTopic planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
