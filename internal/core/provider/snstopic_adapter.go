// SNSTopic provider adapter.
//
// This file implements the provider.Adapter interface for Amazon SNS (Topic)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the SNSTopic Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + topic name.
// SNS topics are region-scoped; the key combines the AWS region and topic name.
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

// SNSTopicAdapter implements provider.Adapter for SNSTopic (Amazon SNS (Topic)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type SNSTopicAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI snstopic.TopicAPI
	apiFactory        func(aws.Config) snstopic.TopicAPI
}

// NewSNSTopicAdapterWithAuth creates a production SNSTopic adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewSNSTopicAdapterWithAuth(auth authservice.AuthClient) *SNSTopicAdapter {
	return &SNSTopicAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) snstopic.TopicAPI {
			return snstopic.NewTopicAPI(awsclient.NewSNSClient(cfg))
		},
	}
}

// NewSNSTopicAdapterWithAPI creates a SNSTopic adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewSNSTopicAdapterWithAPI(api snstopic.TopicAPI) *SNSTopicAdapter {
	return &SNSTopicAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "SNSTopic" that maps template
// resource documents to this adapter in the provider registry.
func (a *SNSTopicAdapter) Kind() string        { return snstopic.ServiceName }
// ServiceName returns the Restate Virtual Object service name for the
// SNSTopic driver. The orchestrator uses this to dispatch durable RPCs.
func (a *SNSTopicAdapter) ServiceName() string { return snstopic.ServiceName }
// Scope returns the key-scope strategy for SNSTopic resources,
// which controls how BuildKey assembles the canonical object key.
func (a *SNSTopicAdapter) Scope() KeyScope     { return KeyScopeRegion }

// BuildKey derives the canonical Restate object key for a SNSTopic resource
// from the raw JSON resource document. The key is composed of region + topic name,
// ensuring uniqueness within the Restate Virtual Object namespace.
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

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete SNSTopic spec struct expected by the driver.
func (a *SNSTopicAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the SNSTopic Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
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

// Delete sends a durable Delete request to the SNSTopic Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *SNSTopicAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed SNSTopic driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
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

// Plan compares the desired SNSTopic spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
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

// BuildImportKey derives the canonical Restate object key for importing
// an existing SNSTopic resource by its region and provider-native ID.
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

// Import adopts an existing SNSTopic resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
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

// decodeSpec unmarshals the raw JSON spec from a resource document into
// the typed SNSTopic spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
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

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
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
