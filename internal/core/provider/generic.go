// generic.go implements GenericAdapter — a descriptor-driven Adapter that
// removes the ~250 lines of identical plumbing each per-resource adapter file
// used to carry (BuildKey → Provision → Delete → Plan → Import → normalize).
//
// A resource adapter becomes a GenericDescriptor: a handful of small closures
// covering only what genuinely varies per kind — spec decoding, key
// derivation, output normalization, and the plan-time describe + diff. The
// generic adapter owns the invariant Restate call wiring.
//
// Adapters are ported to this incrementally; see sqspolicy_adapter.go for the
// reference shape of a ported adapter.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/pkg/types"
)

// PlanProbeInput carries every typed value a plan-time provider lookup may
// need. Identity is the descriptor-selected provider identifier; Desired and
// Outputs remain available for APIs that need a second lookup key or stored
// provider context.
type PlanProbeInput[S any, O any] struct {
	Key      string
	Identity string
	Desired  S
	Outputs  O
}

// PlanProbeFunc looks up the live provider state for a resource during plan.
// The bool reports whether the resource exists; a NotFound from the provider
// must be translated to (zero, false, nil), and other errors returned as-is.
type PlanProbeFunc[S any, O any, Obs any] func(ctx restate.RunContext, input PlanProbeInput[S, O]) (Obs, bool, error)

// LookupProbeFunc resolves an existing provider resource into the driver's
// typed output shape. The bool reports whether a matching resource exists.
// Provider not-found responses must become (zero, false, nil); all other
// errors are returned for classification inside the generic Restate boundary.
type LookupProbeFunc[O any] func(ctx restate.RunContext, filter LookupFilter) (O, bool, error)

// storedPlanIdentity is the common planning strategy for resources that can
// only be described after a provider identifier has been persisted.
func storedPlanIdentity[S any, O any](extract func(O) string) func(S, O) (string, bool) {
	return func(_ S, outputs O) (string, bool) {
		identity := extract(outputs)
		return identity, identity != ""
	}
}

// GenericDescriptor declares everything kind-specific the GenericAdapter
// needs. S is the driver spec type, O the driver outputs type, Obs the
// driver's observed-state type used for plan diffs.
type GenericDescriptor[S any, O any, Obs any] struct {
	// Kind is the template kind and Restate Virtual Object service name
	// (these are the same for every built-in driver).
	Kind string

	// Scope governs how the CLI assembles user input into a canonical key.
	Scope KeyScope

	// DecodeSpec parses the rendered resource document into the driver spec.
	// It owns validation and metadata.name fallbacks.
	DecodeSpec func(spec json.RawMessage, metadataName string) (S, error)

	// KeyFromSpec derives the canonical Virtual Object key from a decoded spec.
	// metadataName carries the resource document's metadata.name for kinds
	// keyed by name rather than spec fields.
	KeyFromSpec func(spec S, metadataName string) (string, error)

	// ImportKey derives the canonical key for an import flow from the
	// user-supplied region and cloud-native resource ID.
	ImportKey func(region, resourceID string) (string, error)

	// PrepareSpec finalizes the spec before dispatch: injecting the account
	// alias and, for drivers that track it, the canonical managed key.
	PrepareSpec func(spec S, key, account string) S

	// NormalizeOutputs converts the typed driver outputs into the generic map
	// stored in deployment state and used for expression hydration.
	NormalizeOutputs func(outputs O) map[string]any

	// PlanIdentity selects the provider-native identifier used during plan and
	// reports whether a live probe is meaningful. Most kinds require a stored
	// provider ID; name-addressable kinds may fall back to the desired name.
	PlanIdentity func(desired S, outputs O) (identity string, probe bool)

	// NewPlanProbe builds the plan-time describe function from AWS config.
	// Implementations wrap the driver's API and its IsNotFound classifier.
	NewPlanProbe func(cfg aws.Config) PlanProbeFunc[S, O, Obs]

	// DiffFields compares the desired spec against observed provider state
	// and returns field-level diffs (typically the driver's ComputeFieldDiffs).
	DiffFields func(desired S, observed Obs, outputs O) []types.FieldDiff

	// NewLookupProbe builds the read-only data-source lookup function from AWS
	// config. It is nil while a resource kind has no implemented lookup probe.
	NewLookupProbe func(cfg aws.Config) LookupProbeFunc[O]

	// SensitiveFields lists spec paths (dot notation, e.g. "spec.secretString")
	// whose values must never appear in plan output. Matching field diffs — on
	// both the create and update paths — have their values replaced with
	// "(sensitive)". Matching is by exact path or dotted prefix, so
	// "spec.value" also masks "spec.value.nested".
	SensitiveFields []string
}

// GenericAdapter implements Adapter for any resource kind described by a
// GenericDescriptor. Construct with NewGenericAdapter (production) or
// NewGenericAdapterWithProbe (tests, static plan probe).
type GenericAdapter[S any, O any, Obs any] struct {
	desc              GenericDescriptor[S, O, Obs]
	auth              authservice.AuthClient
	staticProbe       PlanProbeFunc[S, O, Obs]
	staticLookupProbe LookupProbeFunc[O]
}

// NewGenericAdapter builds a production adapter that resolves plan-time AWS
// credentials through the Auth Service.
func NewGenericAdapter[S any, O any, Obs any](desc GenericDescriptor[S, O, Obs], auth authservice.AuthClient) *GenericAdapter[S, O, Obs] {
	return &GenericAdapter[S, O, Obs]{desc: desc, auth: auth}
}

// NewGenericAdapterWithProbe builds an adapter with a fixed plan probe,
// bypassing credential resolution. Used by tests.
func NewGenericAdapterWithProbe[S any, O any, Obs any](desc GenericDescriptor[S, O, Obs], probe PlanProbeFunc[S, O, Obs]) *GenericAdapter[S, O, Obs] {
	return &GenericAdapter[S, O, Obs]{desc: desc, staticProbe: probe}
}

// NewGenericAdapterWithProbes builds an adapter with fixed plan and lookup
// probes, bypassing credential resolution. Used by provider adapter tests.
func NewGenericAdapterWithProbes[S any, O any, Obs any](
	desc GenericDescriptor[S, O, Obs],
	planProbe PlanProbeFunc[S, O, Obs],
	lookupProbe LookupProbeFunc[O],
) *GenericAdapter[S, O, Obs] {
	return &GenericAdapter[S, O, Obs]{
		desc:              desc,
		staticProbe:       planProbe,
		staticLookupProbe: lookupProbe,
	}
}

func (a *GenericAdapter[S, O, Obs]) Kind() string        { return a.desc.Kind }
func (a *GenericAdapter[S, O, Obs]) ServiceName() string { return a.desc.Kind }
func (a *GenericAdapter[S, O, Obs]) Scope() KeyScope     { return a.desc.Scope }

// genericAdapter marks descriptor-backed adapters for package-level static
// conformance checks. Embedded GenericAdapter values promote this method while
// still allowing a resource to expose optional capabilities such as Lookup.
func (a *GenericAdapter[S, O, Obs]) genericAdapter() {}

func (a *GenericAdapter[S, O, Obs]) lookupConfigured() bool {
	return a.staticLookupProbe != nil || a.desc.NewLookupProbe != nil
}

// SensitiveFields exposes the descriptor's sensitive spec paths so callers
// outside the adapter (e.g. the command-layer expression-resource fallback,
// which builds diffs from raw JSON rather than through Plan) can mask the same
// paths. Returns nil for kinds with no sensitive fields.
func (a *GenericAdapter[S, O, Obs]) SensitiveFields() []string { return a.desc.SensitiveFields }

func (a *GenericAdapter[S, O, Obs]) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.desc.DecodeSpec(doc.Spec, doc.Metadata.Name)
	if err != nil {
		return "", err
	}
	return a.desc.KeyFromSpec(spec, doc.Metadata.Name)
}

func (a *GenericAdapter[S, O, Obs]) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	return a.decodeSpec(resourceDoc)
}

func (a *GenericAdapter[S, O, Obs]) decodeSpec(resourceDoc json.RawMessage) (S, error) {
	var zero S
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return zero, err
	}
	return a.desc.DecodeSpec(doc.Spec, doc.Metadata.Name)
}

func (a *GenericAdapter[S, O, Obs]) Provision(ctx restate.Context, key string, account string, spec any, lifecycle types.LifecyclePolicy) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[S](spec)
	if err != nil {
		return nil, err
	}
	typedSpec = a.desc.PrepareSpec(typedSpec, key, account)

	rawSpec, err := json.Marshal(typedSpec)
	if err != nil {
		return nil, fmt.Errorf("%s Provision: encode spec: %w", a.Kind(), err)
	}
	request := types.ProvisionRequest{
		Spec:      rawSpec,
		Lifecycle: types.NormalizeLifecyclePolicy(&lifecycle),
	}
	fut := restate.WithRequestType[types.ProvisionRequest, O](
		restate.Object[O](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(request)

	return &provisionHandle[O]{
		id:        fut.GetInvocationId(),
		raw:       fut,
		normalize: a.NormalizeOutputs,
	}, nil
}

func (a *GenericAdapter[S, O, Obs]) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *GenericAdapter[S, O, Obs]) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[O](raw)
	if err != nil {
		return nil, err
	}
	return a.desc.NormalizeOutputs(out), nil
}

func (a *GenericAdapter[S, O, Obs]) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[S](desiredSpec)
	if err != nil {
		return "", nil, err
	}

	outputs, getErr := restate.Object[O](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("%s Plan: failed to read outputs for key %q: %w", a.Kind(), key, getErr)
	}
	identity, shouldProbe := a.desc.PlanIdentity(desired, outputs)
	if !shouldProbe {
		return planCreate(desired, a.desc.SensitiveFields)
	}

	probe, err := a.planProbe(ctx, account)
	if err != nil {
		return "", nil, err
	}

	type describePlanResult struct {
		State Obs  `json:"state"`
		Found bool `json:"found"`
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		state, found, describeErr := probe(runCtx, PlanProbeInput[S, O]{
			Key:      key,
			Identity: identity,
			Desired:  desired,
			Outputs:  outputs,
		})
		if describeErr != nil {
			return describePlanResult{}, classifyPlanProbeError(describeErr)
		}
		return describePlanResult{State: state, Found: found}, nil
	})
	if err != nil {
		return "", nil, err
	}
	if !result.Found {
		return planCreate(desired, a.desc.SensitiveFields)
	}

	fields := a.desc.DiffFields(desired, result.State, outputs)
	if len(fields) == 0 {
		return types.OpNoOp, nil, nil
	}
	return types.OpUpdate, types.MaskSensitiveFieldDiffs(fields, a.desc.SensitiveFields), nil
}

func classifyPlanProbeError(err error) error {
	if awserr.IsExpiredToken(err) {
		// The probe's credentials were resolved before entering restate.Run;
		// retrying this same durable call cannot refresh them.
		return restate.TerminalError(err, 401)
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	// Throttling, transport failures, and unknown provider errors are
	// retryable. Not-found/conflict semantics stay in the typed probe.
	return err
}

func (a *GenericAdapter[S, O, Obs]) BuildImportKey(region, resourceID string) (string, error) {
	return a.desc.ImportKey(region, resourceID)
}

func (a *GenericAdapter[S, O, Obs]) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, O](
		restate.Object[O](ctx, a.ServiceName(), key, "Import"),
	).Request(ref)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, a.desc.NormalizeOutputs(output), nil
}

// Lookup performs a read-only provider lookup and returns normalized resource
// outputs without creating driver state. Every implemented lookup uses this
// path so credential resolution, region selection, durable execution, and
// error classification stay uniform across resource kinds.
func (a *GenericAdapter[S, O, Obs]) Lookup(ctx restate.Context, account string, filter LookupFilter) (map[string]any, error) {
	if err := validateLookupFilter(filter); err != nil {
		return nil, restate.TerminalError(err, 400)
	}

	probe, effectiveRegion, err := a.lookupProbe(ctx, account, filter.Region)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(filter.Region) == "" {
		filter.Region = effectiveRegion
	}

	type lookupResult struct {
		Outputs O `json:"outputs"`
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (lookupResult, error) {
		outputs, found, lookupErr := probe(runCtx, filter)
		if lookupErr != nil {
			return lookupResult{}, classifyLookupProbeError(lookupErr)
		}
		if !found {
			return lookupResult{}, restate.TerminalError(
				fmt.Errorf("data source lookup: no %s found matching filter", a.Kind()),
				404,
			)
		}
		return lookupResult{Outputs: outputs}, nil
	})
	if err != nil {
		return nil, err
	}
	return a.desc.NormalizeOutputs(result.Outputs), nil
}

func validateLookupFilter(filter LookupFilter) error {
	if strings.TrimSpace(filter.ID) == "" && strings.TrimSpace(filter.Name) == "" && len(filter.Tag) == 0 {
		return fmt.Errorf("data source lookup requires at least one of: id, name, tag")
	}
	return nil
}

func classifyLookupProbeError(err error) error {
	if restate.IsTerminalError(err) {
		return err
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "ambiguous") || strings.Contains(message, "multiple") {
		return restate.TerminalError(err, 409)
	}
	return classifyPlanProbeError(err)
}

// planProbe resolves the describe function for plan-time lookups: the static
// test probe when set, otherwise a fresh probe built from the account's
// resolved AWS credentials.
func (a *GenericAdapter[S, O, Obs]) planProbe(ctx restate.Context, account string) (PlanProbeFunc[S, O, Obs], error) {
	if a.staticProbe != nil {
		return a.staticProbe, nil
	}
	if a.auth == nil || a.desc.NewPlanProbe == nil {
		return nil, fmt.Errorf("%s adapter planning API is not configured", a.Kind())
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve %s planning account %q: %w", a.Kind(), account, err)
	}
	return a.desc.NewPlanProbe(awsCfg), nil
}

func (a *GenericAdapter[S, O, Obs]) lookupProbe(ctx restate.Context, account, region string) (LookupProbeFunc[O], string, error) {
	if a.staticLookupProbe != nil {
		return a.staticLookupProbe, strings.TrimSpace(region), nil
	}
	if a.auth == nil || a.desc.NewLookupProbe == nil {
		return nil, "", restate.TerminalError(fmt.Errorf("data source lookup is not supported for %q", a.Kind()), 501)
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve %s lookup account %q: %w", a.Kind(), account, err)
	}
	if strings.TrimSpace(region) != "" {
		awsCfg.Region = strings.TrimSpace(region)
	}
	return a.desc.NewLookupProbe(awsCfg), awsCfg.Region, nil
}

// planCreate renders the OpCreate result for a resource with no prior state.
func planCreate(desired any, sensitive []string) (types.DiffOperation, []types.FieldDiff, error) {
	fields, err := createFieldDiffsFromSpec(desired)
	if err != nil {
		return "", nil, err
	}
	return types.OpCreate, types.MaskSensitiveFieldDiffs(fields, sensitive), nil
}
