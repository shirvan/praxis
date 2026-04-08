// EBSVolume provider adapter.
//
// This file implements the provider.Adapter interface for Amazon EBS
// resources. It translates between the generic JSON resource documents used by
// the orchestrator / command service and the strongly typed Go structs expected
// by the EBSVolume Restate Virtual Object driver.
//
// Key scope: region-scoped.
// Key parts: region + volume name.
// EBS volumes are region-scoped; the key combines the AWS region with the volume Name tag.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/ebs"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// EBSAdapter implements provider.Adapter for EBSVolume (Amazon EBS) resources.
// It holds an auth client for credential resolution and a factory for creating
// AWS API clients scoped to the target account. A staticPlanningAPI field allows
// tests to inject a mock API without requiring real AWS credentials.
type EBSAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI ebs.EBSAPI
	apiFactory        func(aws.Config) ebs.EBSAPI
}

// NewEBSAdapterWithAuth creates a production EBSVolume adapter using
// the given auth client for per-account credential resolution.
// The apiFactory closure creates a real AWS API client from the resolved
// aws.Config, ensuring each Plan/Provision call targets the correct account.
func NewEBSAdapterWithAuth(auth authservice.AuthClient) *EBSAdapter {
	return &EBSAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) ebs.EBSAPI {
			return ebs.NewEBSAPI(awsclient.NewEC2Client(cfg))
		},
	}
}

// NewEBSAdapterWithAPI creates a EBSVolume adapter with a pre-built API
// client. This is primarily useful in tests that supply a mock implementation
// and do not need per-account credential resolution.
func NewEBSAdapterWithAPI(api ebs.EBSAPI) *EBSAdapter {
	return &EBSAdapter{staticPlanningAPI: api}
}

// Kind returns the resource kind string "EBSVolume" that maps template
// resource documents to this adapter in the provider registry.
func (a *EBSAdapter) Kind() string {
	return ebs.ServiceName
}

// ServiceName returns the Restate Virtual Object service name for the
// EBSVolume driver. The orchestrator uses this to dispatch durable RPCs.
func (a *EBSAdapter) ServiceName() string {
	return ebs.ServiceName
}

// Scope returns the key-scope strategy for EBSVolume resources,
// which controls how BuildKey assembles the canonical object key.
func (a *EBSAdapter) Scope() KeyScope {
	return KeyScopeRegion
}

// BuildKey derives the canonical Restate object key for a EBSVolume resource
// from the raw JSON resource document. The key is composed of region + volume name,
// ensuring uniqueness within the Restate Virtual Object namespace.
func (a *EBSAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
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
	if err := ValidateKeyPart("volume name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

// DecodeSpec extracts the spec section from a raw JSON resource document and
// returns it as the concrete EBSVolume spec struct expected by the driver.
func (a *EBSAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

// Provision sends a durable Provision request to the EBSVolume Restate
// Virtual Object keyed by the given key. It returns a ProvisionInvocation
// handle that the orchestrator can await via restate.Wait/WaitFirst.
// The account string is injected into the spec so the driver knows which
// AWS account to target.
func (a *EBSAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[ebs.EBSVolumeSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key

	fut := restate.WithRequestType[ebs.EBSVolumeSpec, ebs.EBSVolumeOutputs](
		restate.Object[ebs.EBSVolumeOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)

	return &provisionHandle[ebs.EBSVolumeOutputs]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

// Delete sends a durable Delete request to the EBSVolume Restate Virtual
// Object keyed by the given key. It returns a DeleteInvocation handle
// that the orchestrator can await alongside other parallel futures.
func (a *EBSAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})

	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

// NormalizeOutputs converts the typed EBSVolume driver output struct into
// the generic map[string]any used by deployment state, CLI display,
// and cross-resource expression interpolation.
func (a *EBSAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[ebs.EBSVolumeOutputs](raw)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"volumeId":         out.VolumeId,
		"availabilityZone": out.AvailabilityZone,
		"state":            out.State,
		"sizeGiB":          out.SizeGiB,
		"volumeType":       out.VolumeType,
		"encrypted":        out.Encrypted,
	}
	if out.ARN != "" {
		result["arn"] = out.ARN
	}
	return result, nil
}

// Plan compares the desired EBSVolume spec against the current provider
// state. It first checks whether the resource already exists (via cached
// outputs or a Describe API call), then computes field-level diffs.
// Returns OpCreate if the resource is absent, OpUpdate if fields differ,
// or OpNoOp if the resource matches the desired state.
func (a *EBSAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[ebs.EBSVolumeSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[ebs.EBSVolumeOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("EBS Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.VolumeId == "" {
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
		State ebs.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeVolume(runCtx, outputs.VolumeId)
		if descErr != nil {
			if ebs.IsNotFound(descErr) {
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

	rawDiffs := ebs.ComputeFieldDiffs(desired, result.State)
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
// an existing EBSVolume resource by its region and provider-native ID.
func (a *EBSAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	return JoinKey(region, resourceID), nil
}

// Import adopts an existing EBSVolume resource into Praxis management.
// It delegates to the driver's Import handler and normalizes the outputs.
func (a *EBSAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, ebs.EBSVolumeOutputs](
		restate.Object[ebs.EBSVolumeOutputs](ctx, a.ServiceName(), key, "Import"),
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

// DefaultTimeouts provides per-kind default timeouts for EBS volumes.
func (a *EBSAdapter) DefaultTimeouts() types.ResourceTimeouts {
	return types.ResourceTimeouts{Create: "10m", Update: "10m", Delete: "10m"}
}

// decodeSpec unmarshals the raw JSON spec from a resource document into
// the typed EBSVolume spec struct, validates required fields, and applies
// sensible defaults. The Account field is deliberately zeroed so that only
// the orchestrator (not the template author) can set the target account.
func (a *EBSAdapter) decodeSpec(doc resourceDocument) (ebs.EBSVolumeSpec, error) {
	var spec ebs.EBSVolumeSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return ebs.EBSVolumeSpec{}, fmt.Errorf("decode EBSVolume spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if name == "" {
		return ebs.EBSVolumeSpec{}, fmt.Errorf("EBSVolume metadata.name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return ebs.EBSVolumeSpec{}, fmt.Errorf("EBSVolume spec.region is required")
	}
	if strings.TrimSpace(spec.AvailabilityZone) == "" {
		return ebs.EBSVolumeSpec{}, fmt.Errorf("EBSVolume spec.availabilityZone is required")
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	if spec.Tags["Name"] == "" {
		spec.Tags["Name"] = name
	}
	if spec.VolumeType == "" {
		spec.VolumeType = "gp3"
	}
	if spec.SizeGiB == 0 {
		spec.SizeGiB = 20
	}
	spec.Account = ""
	return spec, nil
}

// planningAPI returns the AWS API client used for Plan (read-only) operations.
// In production it resolves credentials for the given account via the auth
// client and creates a fresh API. In tests it returns the staticPlanningAPI.
func (a *EBSAdapter) planningAPI(ctx restate.Context, account string) (ebs.EBSAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("EBS adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve EBS planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
