// ECRLifecyclePolicy provider adapter.
//
// This file implements the provider.Adapter interface for Amazon ECR (Lifecycle Policy)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the ECRLifecyclePolicy Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + repository name.
// ECR lifecycle policies are region-scoped and tied to a repository; the key combines the AWS region and repository name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/ecrpolicy"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// ECRLifecyclePolicyAdapter implements provider.Adapter for ECRLifecyclePolicy (Amazon ECR (Lifecycle Policy)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type ECRLifecyclePolicyAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI ecrpolicy.LifecyclePolicyAPI
	apiFactory        func(aws.Config) ecrpolicy.LifecyclePolicyAPI
}

// NewECRLifecyclePolicyAdapterWithAuth creates a production ECRLifecyclePolicy adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewECRLifecyclePolicyAdapterWithAuth(auth authservice.AuthClient) *ECRLifecyclePolicyAdapter {
	return &ECRLifecyclePolicyAdapter{auth: auth, apiFactory: func(cfg aws.Config) ecrpolicy.LifecyclePolicyAPI {
		return ecrpolicy.NewLifecyclePolicyAPI(awsclient.NewECRClient(cfg))
	}}
}

// Kind returns the resource kind string "ECRLifecyclePolicy" that maps template
// resource documents to this adapter in the provider registry.
func (a *ECRLifecyclePolicyAdapter) Kind() string        { return ecrpolicy.ServiceName }
// ServiceName returns the Restate Virtual Object service name for the
// ECRLifecyclePolicy driver. The orchestrator uses this to dispatch durable RPCs.
func (a *ECRLifecyclePolicyAdapter) ServiceName() string { return ecrpolicy.ServiceName }
// Scope returns the key-scope strategy for ECRLifecyclePolicy resources,
// which controls how BuildKey assembles the canonical object key.
func (a *ECRLifecyclePolicyAdapter) Scope() KeyScope     { return KeyScopeCustom }

// BuildKey derives the canonical Restate object key for a ECRLifecyclePolicy resource
// from the raw JSON resource document. The key is composed of region + repository name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *ECRLifecyclePolicyAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("repository name", spec.RepositoryName); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.RepositoryName), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete ECRLifecyclePolicy spec struct expected by the driver.
func (a *ECRLifecyclePolicyAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the ECRLifecyclePolicy Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *ECRLifecyclePolicyAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[ecrpolicy.ECRLifecyclePolicySpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[ecrpolicy.ECRLifecyclePolicySpec, ecrpolicy.ECRLifecyclePolicyOutputs](restate.Object[ecrpolicy.ECRLifecyclePolicyOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[ecrpolicy.ECRLifecyclePolicyOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the ECRLifecyclePolicy Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *ECRLifecyclePolicyAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed ECRLifecyclePolicy driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *ECRLifecyclePolicyAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[ecrpolicy.ECRLifecyclePolicyOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"repositoryName": out.RepositoryName}
	if out.RepositoryArn != "" {
		result["repositoryArn"] = out.RepositoryArn
	}
	if out.RegistryId != "" {
		result["registryId"] = out.RegistryId
	}
	return result, nil
}

// Plan compares the desired ECRLifecyclePolicy spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *ECRLifecyclePolicyAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[ecrpolicy.ECRLifecyclePolicySpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[ecrpolicy.ECRLifecyclePolicyOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("ECRLifecyclePolicy Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.RepositoryName == "" {
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
		State ecrpolicy.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.GetLifecyclePolicy(runCtx, outputs.RepositoryName)
		if descErr != nil {
			if ecrpolicy.IsNotFound(descErr) {
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
	rawDiffs := ecrpolicy.ComputeFieldDiffs(desired, result.State)
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
// an existing ECRLifecyclePolicy resource by its region and provider-native ID.
func (a *ECRLifecyclePolicyAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing ECRLifecyclePolicy resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *ECRLifecyclePolicyAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, ecrpolicy.ECRLifecyclePolicyOutputs](restate.Object[ecrpolicy.ECRLifecyclePolicyOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
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
// the typed ECRLifecyclePolicy spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *ECRLifecyclePolicyAdapter) decodeSpec(doc resourceDocument) (ecrpolicy.ECRLifecyclePolicySpec, error) {
	var spec ecrpolicy.ECRLifecyclePolicySpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return ecrpolicy.ECRLifecyclePolicySpec{}, fmt.Errorf("decode ECRLifecyclePolicy spec: %w", err)
	}
	if strings.TrimSpace(spec.Region) == "" {
		return ecrpolicy.ECRLifecyclePolicySpec{}, fmt.Errorf("ECRLifecyclePolicy spec.region is required")
	}
	if strings.TrimSpace(spec.RepositoryName) == "" {
		return ecrpolicy.ECRLifecyclePolicySpec{}, fmt.Errorf("ECRLifecyclePolicy spec.repositoryName is required")
	}
	return spec, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *ECRLifecyclePolicyAdapter) planningAPI(ctx restate.Context, account string) (ecrpolicy.LifecyclePolicyAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("ecr lifecycle policy adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve ECR lifecycle policy planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
