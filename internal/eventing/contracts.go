// Package eventing defines the event contracts shared between Praxis driver
// services and the central event bridge. These types flow over Restate RPC
// and must remain JSON-serializable.
//
// The event flow is:
//
//	Driver (Reconcile detects drift)
//	  → ReportDriftEvent() sends a one-way message to ResourceEventBridge
//	    → ResourceEventBridge fans out to notification sinks (Slack, webhooks)
//
// ResourceEventOwner is a separate concern: it tracks which deployment/stream
// owns a given resource key, so the event bridge can enrich events with
// deployment context before routing them.
package eventing

const (
	// ResourceEventOwnerServiceName is the Restate Virtual Object name for the
	// service that tracks resource → deployment ownership. Keyed by resource key.
	ResourceEventOwnerServiceName = "ResourceEventOwner"

	// ResourceEventBridgeServiceName is the Restate service that receives drift
	// reports and fans them out to registered notification sinks.
	ResourceEventBridgeServiceName = "ResourceEventBridge"

	// DriftEventDetected indicates the driver's Reconcile handler found that
	// the observed cloud state differs from the desired spec. In Managed mode
	// this is followed by a correction attempt; in Observed mode, it is
	// report-only.
	DriftEventDetected = "detected"

	// DriftEventCorrected indicates the driver successfully re-applied the
	// desired spec to bring the resource back into compliance. Only emitted
	// in Managed mode.
	DriftEventCorrected = "corrected"

	// DriftEventExternalDelete indicates the driver discovered that the
	// cloud resource was deleted outside of Praxis (e.g., manual console
	// action or another IaC tool). The resource status transitions to Error.
	DriftEventExternalDelete = "external_delete"
)

// ResourceEventOwner links a driver-level resource key back to its owning
// deployment stream, workspace, and template-local name. The event bridge
// stores one of these per resource key so it can enrich drift events with
// deployment context before routing to notification sinks.
type ResourceEventOwner struct {
	// ResourceKey is the canonical Restate Virtual Object key for the driver
	// instance (e.g., "my-workspace~my-bucket").
	ResourceKey string `json:"resourceKey,omitempty"`

	// StreamKey is the deployment stream identifier that owns this resource.
	// Used to correlate events back to a specific deployment.
	StreamKey string `json:"streamKey"`

	// Workspace is the isolation namespace the resource belongs to.
	Workspace string `json:"workspace,omitempty"`

	// Generation is the monotonically increasing version counter from the
	// driver's state, used to detect stale ownership records.
	Generation int64 `json:"generation,omitempty"`

	// ResourceName is the template-local name (e.g., "webBucket").
	ResourceName string `json:"resourceName"`

	// ResourceKind is the driver type (e.g., "S3Bucket", "SecurityGroup").
	ResourceKind string `json:"resourceKind"`
}

// DriftReportRequest is the payload sent by drivers to the ResourceEventBridge
// when a reconciliation cycle detects drift, corrects it, or discovers an
// external deletion. The bridge uses this to fire CloudEvents to registered
// notification sinks.
type DriftReportRequest struct {
	// ResourceKey is the Restate Virtual Object key identifying the resource.
	ResourceKey string `json:"resourceKey"`

	// ResourceKind is the driver type for display purposes.
	ResourceKind string `json:"resourceKind,omitempty"`

	// EventType is one of DriftEventDetected, DriftEventCorrected, or
	// DriftEventExternalDelete.
	EventType string `json:"eventType"`

	// Error is set when the drift detection or correction itself failed.
	// Empty on successful detection/correction.
	Error string `json:"error,omitempty"`
}
