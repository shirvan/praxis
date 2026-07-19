// Package kernel implements the provider-independent Praxis resource lifecycle.
//
// Resource packages retain ownership of AWS request construction, error
// classification, waiter behavior, normalization, and drift comparison through
// typed Operations and Descriptor implementations. The kernel owns durable
// state transitions and the eight standard Restate handlers.
package kernel

import (
	"fmt"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

const StateVersion = "alpha"

// State is the versioned, atomic lifecycle envelope stored by generic drivers.
// S, O, and Obs remain resource-specific and JSON serializable.
type State[S, O, Obs any] struct {
	Version            string               `json:"version"`
	Desired            S                    `json:"desired"`
	Observed           Obs                  `json:"observed"`
	Outputs            O                    `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Reconcile          types.ReconcileMode  `json:"reconcile"`
	IgnoreChanges      []string             `json:"ignoreChanges,omitempty"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
	Conditions         []types.Condition    `json:"conditions,omitempty"`
}

// Observation is the resource-independent result of a provider read.
type Observation[Obs any] struct {
	Exists bool
	Value  Obs
}

// CreateResult separates durable identity from a one-shot Provision response.
// SeedOutputs may be used to re-observe the created resource and can flow into
// persisted Outputs. CreateOnlyResponse, when non-nil, is a complete response
// returned only by the Provision invocation that called Create. The kernel
// never stores it in State; Restate may still journal the handler response.
type CreateResult[O any] struct {
	SeedOutputs        O
	CreateOnlyResponse *O
}

// Operations contains provider-specific behavior. Implementations must journal
// every AWS call (normally with drivers.RunAWS) and classify provider errors
// inside that journaled callback.
type Operations[S, O, Obs any] interface {
	Observe(ctx restate.ObjectContext, desired S, outputs O) (Observation[Obs], error)
	Create(ctx restate.ObjectContext, desired S) (CreateResult[O], error)
	Converge(ctx restate.ObjectContext, desired S, observed Obs) error
	Delete(ctx restate.ObjectContext, desired S, outputs O) error
	Import(ctx restate.ObjectContext, ref types.ImportRef) (Observation[Obs], error)
}

// ProvisionChangeOperations is an optional provider contract for values that
// cannot be recovered through Observe (for example, write-only credentials).
// The kernel invokes it only from Provision, whenever a prior generation and
// an existing external resource are both present. The implementation compares
// previousDesired with nextDesired and no-ops when no provider write is needed.
// previousDesired remains invocation-local and is never added to State.
type ProvisionChangeOperations[S, Obs any] interface {
	ConvergeProvisionChange(ctx restate.ObjectContext, previousDesired, nextDesired S, observed Obs) error
}

// ReadinessPhase is the provider-independent lifecycle result for an observed
// asynchronous resource.
type ReadinessPhase string

const (
	ReadinessReady   ReadinessPhase = "Ready"
	ReadinessPending ReadinessPhase = "Pending"
	ReadinessFailed  ReadinessPhase = "Failed"
)

// ReadinessResult carries the current asynchronous phase and provider context.
// Message is persisted in conditions for Pending and used as the terminal
// failure detail for Failed.
type ReadinessResult struct {
	Phase   ReadinessPhase
	Message string
}

// Capabilities is an explicit feature manifest. Declared must be true so a
// zero-value manifest cannot silently opt a resource into unsafe assumptions.
type Capabilities struct {
	Declared               bool
	Import                 bool
	ObservedMode           bool
	Delete                 bool
	ManagedDriftCorrection bool
	LateInitialization     bool
	Readiness              bool
	ConvergeWhilePending   bool
}

// Descriptor supplies static resource facts and pure mappings to the kernel.
// Prepare may resolve account configuration, but provider mutations belong in
// Operations.
type Descriptor[S, O, Obs any] struct {
	ServiceName  string
	Capabilities Capabilities
	Operations   Operations[S, O, Obs]

	Prepare  func(ctx restate.ObjectContext, desired S) (S, error)
	Validate func(desired S) error
	// ValidateImport may narrow validation to invariants that imported provider
	// state can satisfy. When omitted, Import uses Validate.
	ValidateImport      func(desired S) error
	DesiredFromObserved func(ref types.ImportRef, observed Obs) S
	OutputsFromObserved func(observed Obs, seed O) O
	HasDrift            func(desired S, observed Obs) bool
	LateInitialize      func(desired S, observed Obs) (S, bool)
	CheckReadiness      func(observed Obs) ReadinessResult
}

func (d Descriptor[S, O, Obs]) ValidateDefinition() error {
	if d.ServiceName == "" {
		return fmt.Errorf("kernel descriptor: service name is required")
	}
	if !d.Capabilities.Declared {
		return fmt.Errorf("kernel descriptor %s: capabilities must be explicitly declared", d.ServiceName)
	}
	if d.Operations == nil {
		return fmt.Errorf("kernel descriptor %s: operations are required", d.ServiceName)
	}
	if d.Capabilities.LateInitialization && d.LateInitialize == nil {
		return fmt.Errorf("kernel descriptor %s: late-initialization capability requires LateInitialize", d.ServiceName)
	}
	if !d.Capabilities.LateInitialization && d.LateInitialize != nil {
		return fmt.Errorf("kernel descriptor %s: LateInitialize requires the late-initialization capability", d.ServiceName)
	}
	if d.Capabilities.Readiness && d.CheckReadiness == nil {
		return fmt.Errorf("kernel descriptor %s: readiness capability requires CheckReadiness", d.ServiceName)
	}
	if !d.Capabilities.Readiness && d.CheckReadiness != nil {
		return fmt.Errorf("kernel descriptor %s: CheckReadiness requires the readiness capability", d.ServiceName)
	}
	if d.Capabilities.ConvergeWhilePending && !d.Capabilities.Readiness {
		return fmt.Errorf("kernel descriptor %s: ConvergeWhilePending requires the readiness capability", d.ServiceName)
	}
	if d.Capabilities.ConvergeWhilePending && !d.Capabilities.ManagedDriftCorrection {
		return fmt.Errorf("kernel descriptor %s: ConvergeWhilePending requires managed drift correction", d.ServiceName)
	}
	if d.Prepare == nil || d.Validate == nil || d.DesiredFromObserved == nil || d.OutputsFromObserved == nil || d.HasDrift == nil {
		return fmt.Errorf("kernel descriptor %s: all lifecycle functions are required", d.ServiceName)
	}
	return nil
}

// New validates a descriptor before it can be registered with Restate.
func New[S, O, Obs any](descriptor Descriptor[S, O, Obs]) (*Driver[S, O, Obs], error) {
	if err := descriptor.ValidateDefinition(); err != nil {
		return nil, err
	}
	return &Driver[S, O, Obs]{descriptor: descriptor}, nil
}

// MustNew is intended for static driver-pack definitions, where an invalid
// descriptor is a startup/programming error rather than a request error.
func MustNew[S, O, Obs any](descriptor Descriptor[S, O, Obs]) *Driver[S, O, Obs] {
	driver, err := New(descriptor)
	if err != nil {
		panic(err)
	}
	return driver
}
