package types

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DeletionPolicy controls what happens to the underlying cloud resource when a
// managed resource is removed from the deployment.
type DeletionPolicy string

const (
	DeletionPolicyDelete DeletionPolicy = "Delete"
	DeletionPolicyOrphan DeletionPolicy = "Orphan"
)

// RetryPolicy configures resource-level retry behavior.
type RetryPolicy struct {
	MaxRetries *int   `json:"maxRetries,omitempty"`
	BaseDelay  string `json:"baseDelay,omitempty"`
	MaxDelay   string `json:"maxDelay,omitempty"`
}

// ResourceTimeouts defines per-operation lifecycle timeouts. Duration values
// are provided as strings so they can round-trip cleanly through rendered JSON.
type ResourceTimeouts struct {
	Create string `json:"create,omitempty"`
	Update string `json:"update,omitempty"`
	Delete string `json:"delete,omitempty"`
}

// ResourceWaitPolicy controls post-provision readiness polling.
type ResourceWaitPolicy struct {
	Enabled      *bool  `json:"enabled,omitempty"`
	PollInterval string `json:"pollInterval,omitempty"`
	MaxWait      string `json:"maxWait,omitempty"`
}

// RecoveryMode controls what Core does when reconciliation finds a managed
// resource in an error state that requires provisioning to recover.
type RecoveryMode string

const (
	RecoveryModeAutomatic RecoveryMode = "Automatic"
	RecoveryModeManual    RecoveryMode = "Manual"
)

// RecoveryPolicy configures Core-owned recovery. Automatic recovery replays
// the current deployment DAG; Manual records the condition and waits for an
// explicit apply. An empty timeout permits retries without a deadline.
type RecoveryPolicy struct {
	Mode    RecoveryMode `json:"mode,omitempty"`
	Timeout string       `json:"timeout,omitempty"`
}

// ReconcileMode controls whether periodic reconciliation may write to the
// provider. Auto restores the declared desired state. Observe reports drift
// without provider mutations and keeps an otherwise healthy resource ready.
type ReconcileMode string

const (
	ReconcileModeAuto    ReconcileMode = "auto"
	ReconcileModeObserve ReconcileMode = "observe"
)

// LifecyclePolicy controls resource-level update and delete behavior.
//
// Templates declare an optional lifecycle block alongside spec. The command
// pipeline extracts it during buildResourceNodes and threads it through to
// the orchestrator, where it influences plan-diff filtering and delete-time
// protection.
type LifecyclePolicy struct {
	// Reconcile controls periodic drift correction. The command pipeline
	// normalizes an omitted value to auto before dispatching the resource.
	Reconcile ReconcileMode `json:"reconcile"`

	// PreventDestroy makes the orchestrator refuse to delete this resource.
	// A delete workflow that encounters a protected resource records an error
	// rather than calling the driver's Delete handler.
	PreventDestroy bool `json:"preventDestroy,omitempty"`

	// IgnoreChanges lists field paths (relative to spec) that the plan diff
	// engine should skip when computing drift. Useful for tags managed by
	// external systems (e.g. cost allocation tags, AWS Config).
	//
	// Paths use dot notation matching the FieldDiff.Path convention,
	// for example "tags.lastModified" or "tags.updatedBy".
	IgnoreChanges []string `json:"ignoreChanges,omitempty"`

	// Retry overrides the deployment's default retry policy for this resource.
	Retry *RetryPolicy `json:"retry,omitempty"`

	// Timeouts overrides create/update/delete timeouts for this resource.
	Timeouts *ResourceTimeouts `json:"timeouts,omitempty"`

	// DeletionPolicy controls whether deletion destroys or orphans the resource.
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// Wait controls post-provision readiness polling.
	Wait *ResourceWaitPolicy `json:"wait,omitempty"`

	// Recovery controls error-state recovery discovered by reconciliation.
	Recovery *RecoveryPolicy `json:"recovery,omitempty"`
}

// NormalizeLifecyclePolicy returns the single alpha lifecycle contract used
// by Core and drivers. The zero-value policy means automatic reconciliation.
func NormalizeLifecyclePolicy(policy *LifecyclePolicy) LifecyclePolicy {
	if policy == nil {
		return LifecyclePolicy{Reconcile: ReconcileModeAuto}
	}
	normalized := *policy
	if normalized.Reconcile == "" {
		normalized.Reconcile = ReconcileModeAuto
	}
	if policy.IgnoreChanges != nil {
		normalized.IgnoreChanges = append([]string(nil), policy.IgnoreChanges...)
	}
	return normalized
}

// ValidateLifecyclePolicy validates the user-facing alpha lifecycle contract.
func ValidateLifecyclePolicy(policy LifecyclePolicy) error {
	if policy.Reconcile != ReconcileModeAuto && policy.Reconcile != ReconcileModeObserve {
		return fmt.Errorf("lifecycle.reconcile must be %q or %q", ReconcileModeAuto, ReconcileModeObserve)
	}
	for _, path := range policy.IgnoreChanges {
		if path == "" || strings.HasPrefix(path, "spec.") || strings.HasPrefix(path, ".") || strings.HasSuffix(path, ".") || strings.Contains(path, "..") {
			return fmt.Errorf("lifecycle.ignoreChanges path %q must be a non-empty dot-separated path relative to spec", path)
		}
	}
	return nil
}

// ProvisionRequest is the alpha driver Provision contract. Spec is raw JSON
// because Restate 1.6 requires a non-generic top-level request type for a valid
// discovery schema; the generic kernel decodes it into its concrete S type.
// Keeping lifecycle policy beside the provider spec lets the kernel persist one
// reconciliation policy without resource-local fields or side-channel handlers.
type ProvisionRequest struct {
	Spec      json.RawMessage `json:"spec"`
	Lifecycle LifecyclePolicy `json:"lifecycle"`
}

// ResourceNode represents a single node in the deployment dependency graph.
//
// The command service produces these nodes after template rendering, CUE
// evaluation, and SSM resolution. The orchestrator then consumes them to
// schedule resources in dependency order.
type ResourceNode struct {
	// Name is the template-local identifier of the resource. Dependency edges use
	// this name rather than the driver key because users reason about templates in
	// terms of logical resource names.
	Name string `json:"name"`

	// Kind is the resource kind and maps directly to an adapter / driver service.
	Kind string `json:"kind"`

	// Key is the canonical Restate Virtual Object key for the target driver.
	Key string `json:"key"`

	// Spec is the fully rendered resource document as raw JSON.
	//
	// The document still contains the full envelope emitted by the template
	// pipeline, such as apiVersion, kind, metadata, and spec. Some values may
	// still carry unresolved dispatch-time expressions if they depend on
	// outputs from upstream resources.
	Spec json.RawMessage `json:"spec"`

	// Dependencies lists the template-local resource names that must complete
	// before this node can be hydrated and dispatched.
	Dependencies []string `json:"dependencies"`

	// Expressions maps a JSON path to the expression that should be resolved
	// at dispatch time.
	//
	// Example:
	//   Key:   "spec.vpcSecurityGroupIds.0"
	//   Value: "resources.sg.outputs.groupId"
	//
	// This lets the orchestrator perform typed replacement at the exact JSON
	// location without rescanning the entire document for placeholders.
	Expressions map[string]string `json:"expressions,omitempty"`

	// Lifecycle holds optional resource-level lifecycle rules parsed from the
	// template. Nil when the template does not declare a lifecycle block.
	Lifecycle *LifecyclePolicy `json:"lifecycle,omitempty"`
}
