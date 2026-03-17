// Package types defines the shared types that flow between Praxis Core and
// driver services over Restate RPC. This is the only public-facing package in the
// repository — external driver service authors import it.
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

	// StatusReady indicates the resource exists and matches the desired state.
	// Reconciliation is active and drift will be detected.
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
//   - Managed: Praxis owns the lifecycle. Drift is detected and auto-corrected.
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

	// Correcting is true only when Drift is true AND the resource is in
	// Managed mode. In Observed mode, drift is reported but Correcting is false.
	Correcting bool `json:"correcting"`

	// Error is set when the reconciliation check itself failed
	// (e.g., AWS API returned an error during describe).
	// This is a string, not error, because it must be JSON-serializable.
	Error string `json:"error,omitempty"`
}

// StatusResponse is returned by the GetStatus shared handler.
// Shared handlers use ObjectSharedContext, allowing concurrent reads
// without blocking exclusive handlers.
type StatusResponse struct {
	Status     ResourceStatus `json:"status"`
	Mode       Mode           `json:"mode"`
	Generation int64          `json:"generation"`
	Error      string         `json:"error,omitempty"`
}
