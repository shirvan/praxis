package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/snssub"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type SNSSubscriptionAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI snssub.SubscriptionAPI
	apiFactory        func(aws.Config) snssub.SubscriptionAPI
}

func NewSNSSubscriptionAdapterWithAuth(auth authservice.AuthClient) *SNSSubscriptionAdapter {
	return &SNSSubscriptionAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) snssub.SubscriptionAPI {
			return snssub.NewSubscriptionAPI(awsclient.NewSNSClient(cfg))
		},
	}
}

func NewSNSSubscriptionAdapterWithAPI(api snssub.SubscriptionAPI) *SNSSubscriptionAdapter {
	return &SNSSubscriptionAdapter{staticPlanningAPI: api}
}

func (a *SNSSubscriptionAdapter) Kind() string        { return snssub.ServiceName }
func (a *SNSSubscriptionAdapter) ServiceName() string { return snssub.ServiceName }
func (a *SNSSubscriptionAdapter) Scope() KeyScope     { return KeyScopeCustom }

// BuildKey returns a key of the form region~topicArn~protocol~endpoint.
func (a *SNSSubscriptionAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("topicArn", spec.TopicArn); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("protocol", spec.Protocol); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("endpoint", spec.Endpoint); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.TopicArn, spec.Protocol, spec.Endpoint), nil
}

func (a *SNSSubscriptionAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *SNSSubscriptionAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[snssub.SNSSubscriptionSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[snssub.SNSSubscriptionSpec, snssub.SNSSubscriptionOutputs](
		restate.Object[snssub.SNSSubscriptionOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[snssub.SNSSubscriptionOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *SNSSubscriptionAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *SNSSubscriptionAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[snssub.SNSSubscriptionOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"subscriptionArn": out.SubscriptionArn,
		"topicArn":        out.TopicArn,
		"protocol":        out.Protocol,
		"endpoint":        out.Endpoint,
	}
	if out.Owner != "" {
		result["owner"] = out.Owner
	}
	return result, nil
}

func (a *SNSSubscriptionAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[snssub.SNSSubscriptionSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[snssub.SNSSubscriptionOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("SNSSubscription Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.SubscriptionArn == "" {
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
		State snssub.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.GetSubscriptionAttributes(runCtx, outputs.SubscriptionArn)
		if descErr != nil {
			if snssub.IsNotFound(descErr) {
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

	rawDiffs := snssub.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

// BuildImportKey returns a key of the form region~subscriptionArn.
// The driver itself resolves the subscription details from the ARN.
func (a *SNSSubscriptionAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("subscription ARN", resourceID); err != nil {
		return "", err
	}
	// resourceID is the subscription ARN; the driver resolves topic/protocol/endpoint from it.
	return JoinKey(region, resourceID), nil
}

func (a *SNSSubscriptionAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, snssub.SNSSubscriptionOutputs](
		restate.Object[snssub.SNSSubscriptionOutputs](ctx, a.ServiceName(), key, "Import"),
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

func (a *SNSSubscriptionAdapter) decodeSpec(doc resourceDocument) (snssub.SNSSubscriptionSpec, error) {
	var spec snssub.SNSSubscriptionSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return snssub.SNSSubscriptionSpec{}, fmt.Errorf("decode SNSSubscription spec: %w", err)
	}
	if strings.TrimSpace(spec.Region) == "" {
		return snssub.SNSSubscriptionSpec{}, fmt.Errorf("SNSSubscription spec.region is required")
	}
	if strings.TrimSpace(spec.TopicArn) == "" {
		return snssub.SNSSubscriptionSpec{}, fmt.Errorf("SNSSubscription spec.topicArn is required")
	}
	if strings.TrimSpace(spec.Protocol) == "" {
		return snssub.SNSSubscriptionSpec{}, fmt.Errorf("SNSSubscription spec.protocol is required")
	}
	if strings.TrimSpace(spec.Endpoint) == "" {
		return snssub.SNSSubscriptionSpec{}, fmt.Errorf("SNSSubscription spec.endpoint is required")
	}
	spec.Account = ""
	return spec, nil
}

func (a *SNSSubscriptionAdapter) planningAPI(ctx restate.Context, account string) (snssub.SubscriptionAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("SNSSubscription adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve SNSSubscription planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
