// ECRRepository provider adapter.
//
// This file implements the provider.Adapter interface for Amazon ECR
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the ECRRepository Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + repository name.
// ECR repositories are region-scoped; the key combines the AWS region and repository name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/ecrrepo"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// ECRRepositoryAdapter implements provider.Adapter for ECRRepository (Amazon ECR) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type ECRRepositoryAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI ecrrepo.RepositoryAPI
	apiFactory        func(aws.Config) ecrrepo.RepositoryAPI
}

// NewECRRepositoryAdapterWithAuth creates a production ECRRepository adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewECRRepositoryAdapterWithAuth(auth authservice.AuthClient) *ECRRepositoryAdapter {
	return &ECRRepositoryAdapter{auth: auth, apiFactory: func(cfg aws.Config) ecrrepo.RepositoryAPI { return ecrrepo.NewRepositoryAPI(awsclient.NewECRClient(cfg)) }}
}

// Kind returns the resource kind string "ECRRepository" that maps template
// resource documents to this adapter in the provider registry.
func (a *ECRRepositoryAdapter) Kind() string        { return ecrrepo.ServiceName }
// ServiceName returns the Restate Virtual Object service name for the
// ECRRepository driver. The orchestrator uses this to dispatch durable RPCs.
func (a *ECRRepositoryAdapter) ServiceName() string { return ecrrepo.ServiceName }
// Scope returns the key-scope strategy for ECRRepository resources,
// which controls how BuildKey assembles the canonical object key.
func (a *ECRRepositoryAdapter) Scope() KeyScope     { return KeyScopeRegion }

// BuildKey derives the canonical Restate object key for a ECRRepository resource
// from the raw JSON resource document. The key is composed of region + repository name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *ECRRepositoryAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
// returns it as the concrete ECRRepository spec struct expected by the driver.
func (a *ECRRepositoryAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the ECRRepository Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *ECRRepositoryAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[ecrrepo.ECRRepositorySpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[ecrrepo.ECRRepositorySpec, ecrrepo.ECRRepositoryOutputs](restate.Object[ecrrepo.ECRRepositoryOutputs](ctx, a.ServiceName(), key, "Provision")).RequestFuture(typedSpec)
	return &provisionHandle[ecrrepo.ECRRepositoryOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

// Delete sends a durable Delete request to the ECRRepository Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *ECRRepositoryAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete")).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed ECRRepository driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *ECRRepositoryAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[ecrrepo.ECRRepositoryOutputs](raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"repositoryArn": out.RepositoryArn, "repositoryName": out.RepositoryName, "repositoryUri": out.RepositoryUri, "registryId": out.RegistryId}, nil
}

// Plan compares the desired ECRRepository spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *ECRRepositoryAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[ecrrepo.ECRRepositorySpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[ecrrepo.ECRRepositoryOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("ECRRepository Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.RepositoryArn == "" {
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
		State ecrrepo.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeRepository(runCtx, outputs.RepositoryName)
		if descErr != nil {
			if ecrrepo.IsNotFound(descErr) {
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
	rawDiffs := ecrrepo.ComputeFieldDiffs(desired, result.State)
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
// an existing ECRRepository resource by its region and provider-native ID.
func (a *ECRRepositoryAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing ECRRepository resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *ECRRepositoryAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, ecrrepo.ECRRepositoryOutputs](restate.Object[ecrrepo.ECRRepositoryOutputs](ctx, a.ServiceName(), key, "Import")).Request(ref)
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
// the typed ECRRepository spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *ECRRepositoryAdapter) decodeSpec(doc resourceDocument) (ecrrepo.ECRRepositorySpec, error) {
	var spec ecrrepo.ECRRepositorySpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return ecrrepo.ECRRepositorySpec{}, fmt.Errorf("decode ECRRepository spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return ecrrepo.ECRRepositorySpec{}, fmt.Errorf("ECRRepository metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return ecrrepo.ECRRepositorySpec{}, fmt.Errorf("ECRRepository spec.region is required")
	}
	spec.RepositoryName = name
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *ECRRepositoryAdapter) planningAPI(ctx restate.Context, account string) (ecrrepo.RepositoryAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("ecr repository adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve ECR repository planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}