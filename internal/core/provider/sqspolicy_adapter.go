package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/sqspolicy"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type SQSQueuePolicyAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI sqspolicy.PolicyAPI
	apiFactory        func(aws.Config) sqspolicy.PolicyAPI
}

func NewSQSQueuePolicyAdapterWithAuth(auth authservice.AuthClient) *SQSQueuePolicyAdapter {
	return &SQSQueuePolicyAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) sqspolicy.PolicyAPI {
			return sqspolicy.NewPolicyAPI(awsclient.NewSQSClient(cfg))
		},
	}
}

func NewSQSQueuePolicyAdapterWithAPI(api sqspolicy.PolicyAPI) *SQSQueuePolicyAdapter {
	return &SQSQueuePolicyAdapter{staticPlanningAPI: api}
}

func (a *SQSQueuePolicyAdapter) Kind() string        { return sqspolicy.ServiceName }
func (a *SQSQueuePolicyAdapter) ServiceName() string { return sqspolicy.ServiceName }
func (a *SQSQueuePolicyAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *SQSQueuePolicyAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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

func (a *SQSQueuePolicyAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *SQSQueuePolicyAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[sqspolicy.SQSQueuePolicySpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[sqspolicy.SQSQueuePolicySpec, sqspolicy.SQSQueuePolicyOutputs](
		restate.Object[sqspolicy.SQSQueuePolicyOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[sqspolicy.SQSQueuePolicyOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *SQSQueuePolicyAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *SQSQueuePolicyAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[sqspolicy.SQSQueuePolicyOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"queueUrl":  out.QueueUrl,
		"queueArn":  out.QueueArn,
		"queueName": out.QueueName,
	}, nil
}

func (a *SQSQueuePolicyAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[sqspolicy.SQSQueuePolicySpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[sqspolicy.SQSQueuePolicyOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("SQSQueuePolicy Plan: failed to read outputs for key %q: %w", key, getErr)
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
		State sqspolicy.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.GetQueuePolicy(runCtx, outputs.QueueUrl)
		if descErr != nil {
			if sqspolicy.IsNotFound(descErr) {
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

	rawDiffs := sqspolicy.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

func (a *SQSQueuePolicyAdapter) BuildImportKey(region, resourceID string) (string, error) {
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

func (a *SQSQueuePolicyAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, sqspolicy.SQSQueuePolicyOutputs](
		restate.Object[sqspolicy.SQSQueuePolicyOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *SQSQueuePolicyAdapter) decodeSpec(doc resourceDocument) (sqspolicy.SQSQueuePolicySpec, error) {
	var parsed struct {
		Region    string          `json:"region"`
		QueueName string          `json:"queueName"`
		Policy    json.RawMessage `json:"policy"`
	}
	if err := json.Unmarshal(doc.Spec, &parsed); err != nil {
		return sqspolicy.SQSQueuePolicySpec{}, fmt.Errorf("decode SQSQueuePolicy spec: %w", err)
	}
	queueName := strings.TrimSpace(parsed.QueueName)
	if queueName == "" {
		queueName = strings.TrimSpace(doc.Metadata.Name)
	}
	if strings.TrimSpace(parsed.Region) == "" {
		return sqspolicy.SQSQueuePolicySpec{}, fmt.Errorf("SQSQueuePolicy spec.region is required")
	}
	if queueName == "" {
		return sqspolicy.SQSQueuePolicySpec{}, fmt.Errorf("SQSQueuePolicy spec.queueName or metadata.name is required")
	}
	if len(parsed.Policy) == 0 {
		return sqspolicy.SQSQueuePolicySpec{}, fmt.Errorf("SQSQueuePolicy spec.policy is required")
	}
	return sqspolicy.SQSQueuePolicySpec{Region: parsed.Region, QueueName: queueName, Policy: string(parsed.Policy)}, nil
}

func (a *SQSQueuePolicyAdapter) planningAPI(ctx restate.Context, account string) (sqspolicy.PolicyAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("SQSQueuePolicy adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve SQSQueuePolicy planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
