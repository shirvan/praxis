// SNSSubscription provider adapter.
//
// This file implements the provider.Adapter interface for Amazon SNS (Subscription)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the SNSSubscription Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + subscription name.
// SNS subscriptions are region-scoped; the key combines the AWS region and subscription name.
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

// SNSSubscriptionAdapter implements provider.Adapter for SNSSubscription (Amazon SNS (Subscription)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type SNSSubscriptionAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI snssub.SubscriptionAPI
	apiFactory        func(aws.Config) snssub.SubscriptionAPI
}

// NewSNSSubscriptionAdapterWithAuth creates a production SNSSubscription adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewSNSSubscriptionAdapterWithAuth(auth authservice.AuthClient) *SNSSubscriptionAdapter {
	return &SNSSubscriptionAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) snssub.SubscriptionAPI {
			return snssub.NewSubscriptionAPI(awsclient.NewSNSClient(cfg))
		},
	}
}

// NewSNSSubscriptionAdapterWithAPI creates a SNSSubscription adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewSNSSubscriptionAdapterWithAPI(api snssub.SubscriptionAPI) *SNSSubscriptionAdapter {
	return &SNSSubscriptionAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "SNSSubscription" that maps template
// resource documents to this adapter in the provider registry.
func (a *SNSSubscriptionAdapter) Kind() string { return snssub.ServiceName }

// ServiceName returns the Restate Virtual Object service name for the
// SNSSubscription driver. The orchestrator uses this to dispatch durable RPCs.
func (a *SNSSubscriptionAdapter) ServiceName() string { return snssub.ServiceName }

// Scope returns the key-scope strategy for SNSSubscription resources,
// which controls how BuildKey assembles the canonical object key.
func (a *SNSSubscriptionAdapter) Scope() KeyScope { return KeyScopeCustom }

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

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete SNSSubscription spec struct expected by the driver.
func (a *SNSSubscriptionAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the SNSSubscription Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
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

// Delete sends a durable Delete request to the SNSSubscription Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *SNSSubscriptionAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed SNSSubscription driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
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

// Plan compares the desired SNSSubscription spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
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

// Import adopts an existing SNSSubscription resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
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

// decodeSpec unmarshals the raw JSON spec from a resource document into
// the typed SNSSubscription spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
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

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
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
