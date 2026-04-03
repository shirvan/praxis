// KeyPair provider adapter.
//
// This file implements the provider.Adapter interface for Amazon EC2 (Key Pair)
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the KeyPair Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + key pair name.
// Key pairs are region-scoped; the key combines the AWS region and the key pair name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/keypair"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// KeyPairAdapter implements provider.Adapter for KeyPair (Amazon EC2 (Key Pair)) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type KeyPairAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI keypair.KeyPairAPI
	apiFactory        func(aws.Config) keypair.KeyPairAPI
}

// NewKeyPairAdapterWithAuth creates a production KeyPair adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewKeyPairAdapterWithAuth(auth authservice.AuthClient) *KeyPairAdapter {
	return &KeyPairAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) keypair.KeyPairAPI {
			return keypair.NewKeyPairAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

// NewKeyPairAdapterWithAPI creates a KeyPair adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewKeyPairAdapterWithAPI(api keypair.KeyPairAPI) *KeyPairAdapter {
	return &KeyPairAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "KeyPair" that maps template
// resource documents to this adapter in the provider registry.
func (a *KeyPairAdapter) Kind() string {
	return keypair.ServiceName
}

// ServiceName returns the Restate Virtual Object service name for the
// KeyPair driver. The orchestrator uses this to dispatch durable RPCs.
func (a *KeyPairAdapter) ServiceName() string {
	return keypair.ServiceName
}

// Scope returns the key-scope strategy for KeyPair resources,
// which controls how BuildKey assembles the canonical object key.
func (a *KeyPairAdapter) Scope() KeyScope {
	return KeyScopeRegion
}

// BuildKey derives the canonical Restate object key for a KeyPair resource
// from the raw JSON resource document. The key is composed of region + key pair name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *KeyPairAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("key pair name", spec.KeyName); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, spec.KeyName), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete KeyPair spec struct expected by the driver.
func (a *KeyPairAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the KeyPair Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *KeyPairAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[keypair.KeyPairSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account

	fut := restate.WithRequestType[keypair.KeyPairSpec, keypair.KeyPairOutputs](
		restate.Object[keypair.KeyPairOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[keypair.KeyPairOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

// Delete sends a durable Delete request to the KeyPair Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *KeyPairAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})

	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed KeyPair driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *KeyPairAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[keypair.KeyPairOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"keyName":        out.KeyName,
		"keyPairId":      out.KeyPairId,
		"keyFingerprint": out.KeyFingerprint,
		"keyType":        out.KeyType,
	}
	if out.PrivateKeyMaterial != "" {
		result["privateKeyMaterial"] = out.PrivateKeyMaterial
	}
	return result, nil
}

// Plan compares the desired KeyPair spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *KeyPairAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[keypair.KeyPairSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[keypair.KeyPairOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("KeyPair Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.KeyName == "" {
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
		State keypair.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeKeyPair(runCtx, outputs.KeyName)
		if descErr != nil {
			if keypair.IsNotFound(descErr) {
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

	rawDiffs := keypair.ComputeFieldDiffs(desired, result.State)
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
// an existing KeyPair resource by its region and provider-native ID.
func (a *KeyPairAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing KeyPair resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *KeyPairAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, keypair.KeyPairOutputs](
		restate.Object[keypair.KeyPairOutputs](ctx, a.ServiceName(), key, "Import"),
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
// the typed KeyPair spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *KeyPairAdapter) decodeSpec(doc resourceDocument) (keypair.KeyPairSpec, error) {
	var spec keypair.KeyPairSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return keypair.KeyPairSpec{}, fmt.Errorf("decode KeyPair spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return keypair.KeyPairSpec{}, fmt.Errorf("KeyPair metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return keypair.KeyPairSpec{}, fmt.Errorf("KeyPair spec.region is required")
	}
	if spec.KeyType == "" {
		spec.KeyType = "ed25519"
	}
	if spec.KeyType != "rsa" && spec.KeyType != "ed25519" {
		return keypair.KeyPairSpec{}, fmt.Errorf("KeyPair spec.keyType must be \"rsa\" or \"ed25519\"")
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	spec.KeyName = name
	spec.Account = ""
	return spec, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *KeyPairAdapter) planningAPI(ctx restate.Context, account string) (keypair.KeyPairAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("KeyPair adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve KeyPair planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
