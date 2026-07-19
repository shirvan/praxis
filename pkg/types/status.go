// Package types defines the shared types that flow between Praxis Core and
// built-in driver services over Restate RPC.
//
// All types must be JSON-serializable since Restate uses encoding/json by
// default for handler input/output serialization.
package types

// ResourceStatus represents the current lifecycle status of a managed resource.
// Every driver service uses these same status values, which enables Core's
// orchestrator to treat all drivers uniformly when checking deployment progress.
//
// The status machine is:
//
//	Pending → Provisioning → Ready
//	                       → Error
//	Ready   → Deleting     → Deleted
//	Ready   → Error        (external deletion, reconcile failure)
//	Error   → Provisioning (user re-triggers Provision)
type ResourceStatus string

const (
	// StatusPending indicates the resource has been declared but provisioning
	// has not yet started. This is the zero-value state for a new Virtual Object.
	StatusPending ResourceStatus = "Pending"

	// StatusProvisioning indicates an active Provision or Import operation.
	// The resource is being created or updated in the cloud provider.
	StatusProvisioning ResourceStatus = "Provisioning"

	// StatusReady indicates the resource exists and its lifecycle is healthy.
	// ConditionDriftFree reports whether provider state currently matches the
	// declared settings; drift can coexist with Ready when correction is disabled.
	StatusReady ResourceStatus = "Ready"

	// StatusError indicates a permanent failure. The resource may or may not
	// exist in the cloud provider. Reconciliation continues in read-only mode
	// to provide visibility. User must re-trigger Provision to recover.
	StatusError ResourceStatus = "Error"

	// StatusDeleting indicates an active Delete operation.
	StatusDeleting ResourceStatus = "Deleting"

	// StatusDeleted is a tombstone — the resource has been removed.
	// GetStatus returns this rather than an empty response.
	StatusDeleted ResourceStatus = "Deleted"
)

// Mode determines how a resource is managed during reconciliation.
//
//   - Managed: Praxis owns provisioning and deletion. Periodic writes follow
//     the resource's lifecycle.reconcile policy.
//   - Observed: Praxis tracks the resource but never modifies it. Drift is
//     reported but not corrected. Useful for monitoring resources managed by
//     another system while gradually migrating to Praxis.
type Mode string

const (
	ModeManaged  Mode = "Managed"
	ModeObserved Mode = "Observed"
)

// ReconcileResult is returned by every driver's Reconcile handler.
// Core aggregates these across all resources in a deployment for drift reporting.
type ReconcileResult struct {
	// Drift is true when observed state differs from desired state.
	Drift bool `json:"drift"`

	// Correcting is true only when Drift is true and Praxis is actively writing
	// a correction. Observed resources and resources with correction disabled
	// report drift with Correcting false.
	Correcting bool `json:"correcting"`

	// Error is set when the reconciliation check itself failed
	// (e.g., AWS API returned an error during describe).
	// This is a string, not error, because it must be JSON-serializable.
	Error string `json:"error,omitempty"`

	// Conditions carries structured status conditions from the driver back to
	// the orchestrator. Standard condition types are Healthy and DriftFree.
	Conditions []Condition `json:"conditions,omitempty"`

	// ReplacementRequired tells Core that recovery must replay the deployment
	// DAG instead of recreating the resource inside an isolated driver.
	ReplacementRequired bool `json:"replacementRequired,omitempty"`
}

// StatusResponse is returned by the GetStatus shared handler.
// Shared handlers use ObjectSharedContext, allowing concurrent reads
// without blocking exclusive handlers.
type StatusResponse struct {
	Status        ResourceStatus `json:"status"`
	Mode          Mode           `json:"mode"`
	Reconcile     ReconcileMode  `json:"reconcile"`
	IgnoreChanges []string       `json:"ignoreChanges,omitempty"`
	Generation    int64          `json:"generation"`
	Error         string         `json:"error,omitempty"`
	Conditions    []Condition    `json:"conditions,omitempty"`
}
