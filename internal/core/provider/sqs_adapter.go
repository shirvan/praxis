package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/sqs"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type SQSAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI sqs.QueueAPI
	apiFactory        func(aws.Config) sqs.QueueAPI
}

func NewSQSAdapterWithAuth(auth authservice.AuthClient) *SQSAdapter {
	return &SQSAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) sqs.QueueAPI {
			return sqs.NewQueueAPI(awsclient.NewSQSClient(cfg))
		},
	}
}

func NewSQSAdapterWithAPI(api sqs.QueueAPI) *SQSAdapter {
	return &SQSAdapter{staticPlanningAPI: api}
}

func (a *SQSAdapter) Kind() string        { return sqs.ServiceName }
func (a *SQSAdapter) ServiceName() string { return sqs.ServiceName }
func (a *SQSAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *SQSAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("queue name", spec.QueueName); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.QueueName), nil
}

func (a *SQSAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *SQSAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[sqs.SQSQueueSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[sqs.SQSQueueSpec, sqs.SQSQueueOutputs](
		restate.Object[sqs.SQSQueueOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[sqs.SQSQueueOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *SQSAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *SQSAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[sqs.SQSQueueOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"queueUrl":  out.QueueUrl,
		"queueArn":  out.QueueArn,
		"queueName": out.QueueName,
	}, nil
}

func (a *SQSAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[sqs.SQSQueueSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[sqs.SQSQueueOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("SQSQueue Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.QueueUrl == "" {
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
		State sqs.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.GetQueueAttributes(runCtx, outputs.QueueUrl)
		if descErr != nil {
			if sqs.IsNotFound(descErr) {
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

	rawDiffs := sqs.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *SQSAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	queueName := resourceID
	if strings.HasPrefix(resourceID, "http://") || strings.HasPrefix(resourceID, "https://") {
		parts := strings.Split(resourceID, "/")
		queueName = parts[len(parts)-1]
	}
	if err := ValidateKeyPart("queue name", queueName); err != nil {
		return "", err
	}
	return JoinKey(region, queueName), nil
}

func (a *SQSAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, sqs.SQSQueueOutputs](
		restate.Object[sqs.SQSQueueOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *SQSAdapter) decodeSpec(doc resourceDocument) (sqs.SQSQueueSpec, error) {
	var spec sqs.SQSQueueSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return sqs.SQSQueueSpec{}, fmt.Errorf("decode SQSQueue spec: %w", err)
	}
	if strings.TrimSpace(spec.Region) == "" {
		return sqs.SQSQueueSpec{}, fmt.Errorf("SQSQueue spec.region is required")
	}
	if strings.TrimSpace(spec.QueueName) == "" {
		spec.QueueName = strings.TrimSpace(doc.Metadata.Name)
	}
	if strings.TrimSpace(spec.QueueName) == "" {
		return sqs.SQSQueueSpec{}, fmt.Errorf("SQSQueue spec.queueName or metadata.name is required")
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	if spec.MessageRetentionPeriod == 0 {
		spec.MessageRetentionPeriod = 345600
	}
	if spec.MaximumMessageSize == 0 {
		spec.MaximumMessageSize = 262144
	}
	if spec.VisibilityTimeout == 0 {
		spec.VisibilityTimeout = 30
	}
	if spec.KmsMasterKeyId == "" {
		spec.SqsManagedSseEnabled = true
	}
	if spec.KmsMasterKeyId != "" && spec.KmsDataKeyReusePeriodSeconds == 0 {
		spec.KmsDataKeyReusePeriodSeconds = 300
	}
	spec.Account = ""
	return spec, nil
}

func (a *SQSAdapter) planningAPI(ctx restate.Context, account string) (sqs.QueueAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("SQSQueue adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve SQSQueue planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
