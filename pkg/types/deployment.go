package types

import "time"

// ResourceOutputs is a generic map of output key-value pairs produced by
// a driver after provisioning. Core collects these and uses them to
// hydrate expressions in dependent resources.
//
// Each driver also defines its own strongly-typed outputs struct
// (for example, S3BucketOutputs and SecurityGroupOutputs) and returns that
// from its handlers. This generic wrapper exists for the places where Praxis
// Core needs to reason about outputs from any driver uniformly.
type ResourceOutputs struct {
	Values map[string]any `json:"values"`
}

// DeploymentStatus represents the lifecycle of a deployment as seen by the
// command service, orchestrator, and CLI.
//
// A deployment is the higher-level unit that groups multiple resources under
// one apply/plan/delete workflow. This state machine is intentionally separate
// from driver-level ResourceStatus because a deployment can be Running or
// Cancelled even while individual resources are still Pending, Ready, or Error.
type DeploymentStatus string

const (
	// DeploymentPending means the request was accepted but orchestration has not
	// started dispatching resources yet.
	DeploymentPending DeploymentStatus = "Pending"

	// DeploymentRunning means the orchestrator is actively scheduling or waiting
	// on resource operations.
	DeploymentRunning DeploymentStatus = "Running"

	// DeploymentComplete means every required resource reached a successful end
	// state for the requested operation.
	DeploymentComplete DeploymentStatus = "Complete"

	// DeploymentFailed means at least one required resource failed and the
	// overall deployment can no longer succeed without user intervention.
	DeploymentFailed DeploymentStatus = "Failed"

	// DeploymentDeleting means Core is driving a deployment-wide delete flow.
	DeploymentDeleting DeploymentStatus = "Deleting"

	// DeploymentDeleted means the deployment itself has been fully torn down.
	DeploymentDeleted DeploymentStatus = "Deleted"

	// DeploymentCancelled means the deployment was intentionally stopped before
	// reaching a terminal success state.
	DeploymentCancelled DeploymentStatus = "Cancelled"
)

// DeploymentResourceStatus is the deployment-scoped status for a resource while
// it participates in a larger orchestration flow.
//
// This extends the lower-level driver status machine with deployment-only
// states such as Skipped. A driver never reports Skipped, but the orchestrator
// may mark a resource that way if one of its dependencies fails and execution
// can no longer proceed safely.
type DeploymentResourceStatus string

const (
	// DeploymentResourcePending means the resource is known to the deployment but
	// has not yet been dispatched.
	DeploymentResourcePending DeploymentResourceStatus = "Pending"

	// DeploymentResourceProvisioning means the orchestrator has dispatched the
	// resource to its driver and is waiting for the result.
	DeploymentResourceProvisioning DeploymentResourceStatus = "Provisioning"

	// DeploymentResourceUpdating means the orchestrator has dispatched the
	// resource to its driver for an update (the resource existed in the prior
	// generation). This is semantically identical to Provisioning but provides
	// user-facing clarity that the resource is being updated, not created.
	DeploymentResourceUpdating DeploymentResourceStatus = "Updating"

	// DeploymentResourceReady means the resource completed successfully for the
	// current deployment operation.
	DeploymentResourceReady DeploymentResourceStatus = "Ready"

	// DeploymentResourceError means the resource failed permanently for the
	// current deployment operation.
	DeploymentResourceError DeploymentResourceStatus = "Error"

	// DeploymentResourceSkipped means the resource was intentionally not run,
	// usually because one of its dependencies failed first.
	DeploymentResourceSkipped DeploymentResourceStatus = "Skipped"

	// DeploymentResourceDeleting means the resource is currently being deleted as
	// part of a deployment delete flow.
	DeploymentResourceDeleting DeploymentResourceStatus = "Deleting"

	// DeploymentResourceDeleted means the resource has been removed.
	DeploymentResourceDeleted DeploymentResourceStatus = "Deleted"
)

// DeploymentResource represents a single resource inside a deployment view.
//
// This is the shape returned by Core to the CLI and any future API consumers.
// It intentionally contains generic fields only, because it must describe both
// S3 buckets and security groups without leaking driver-specific Go types.
type DeploymentResource struct {
	// Name is the template-local name of the resource, for example
	// "assetsBucket" or "webSecurityGroup".
	Name string `json:"name"`

	// Kind is the resource kind used to select the driver service, for example
	// "S3Bucket" or "SecurityGroup".
	Kind string `json:"kind"`

	// Key is the canonical Restate Virtual Object key used to address the
	// resource driver instance.
	Key string `json:"key"`

	// Status tracks where this resource currently sits in the deployment
	// orchestration lifecycle.
	Status DeploymentResourceStatus `json:"status"`

	// Outputs contains the generic, normalized driver outputs after successful
	// provisioning. These values are also the source material for dispatch-time
	// expression hydration in dependent resources.
	Outputs map[string]any `json:"outputs,omitempty"`

	// Error stores the terminal failure message, if any. It is intentionally a
	// plain string so the structure stays JSON-serializable and CLI-friendly.
	Error string `json:"error,omitempty"`

	// DependsOn lists template-local resource names that must complete before
	// this resource can be dispatched.
	DependsOn []string `json:"dependsOn,omitempty"`
}

// DeploymentSummary is the compact list item returned by deployment-list APIs.
// It gives the CLI enough information to render status tables without loading
// the full resource graph for every deployment.
type DeploymentSummary struct {
	Key       string           `json:"key"`
	Status    DeploymentStatus `json:"status"`
	Resources int              `json:"resources"`
	Workspace string           `json:"workspace,omitempty"`
	CreatedAt time.Time        `json:"createdAt"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

// DeploymentDetail is the fully expanded deployment record returned by get or
// describe style APIs.
//
// TemplatePath is preserved for operator visibility so a deployment record can
// still point back to the source template location that created it.
type DeploymentDetail struct {
	Key            string               `json:"key"`
	Status         DeploymentStatus     `json:"status"`
	Workspace      string               `json:"workspace,omitempty"`
	TemplatePath   string               `json:"templatePath"`
	Resources      []DeploymentResource `json:"resources"`
	Error          string               `json:"error,omitempty"`
	ErrorCode      ErrorCode            `json:"errorCode,omitempty"`
	ResourceErrors map[string]string    `json:"resourceErrors,omitempty"`
	CreatedAt      time.Time            `json:"createdAt"`
	UpdatedAt      time.Time            `json:"updatedAt"`
}
