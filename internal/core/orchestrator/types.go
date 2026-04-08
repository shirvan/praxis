// Package orchestrator implements the Praxis deployment orchestration engine.
//
// The orchestrator coordinates multi-resource deployments using Restate's durable
// execution framework. It provides three core workflows:
//
//   - DeploymentWorkflow (apply): dispatches resources in DAG dependency order,
//     hydrating cross-resource expressions at dispatch time and tracking each
//     resource through its provisioning lifecycle.
//   - DeploymentDeleteWorkflow: tears down resources in reverse topological
//     order, respecting lifecycle.preventDestroy policies.
//   - DeploymentRollbackWorkflow: selectively deletes only resources that were
//     successfully provisioned (determined from the event store), leaving
//     previously-stable resources untouched.
//
// Durable state is split between Restate Workflows (run-once, keyed by
// invocation) and Virtual Objects (persistent, keyed by deployment). The
// DeploymentState virtual object is the authoritative lifecycle record;
// workflows are transient execution vehicles that read and write it.
//
// All state transitions emit structured CloudEvents through an EventBus →
// EventStore → EventIndex pipeline. Notification sinks (webhooks, structured
// logs, CloudEvents HTTP, Restate RPC) subscribe to these events with per-sink
// filtering and a built-in circuit breaker.
//
// Key subsystems:
//
//   - DAG Scheduler: the dispatch loop consults dag.Schedule to find resources
//     whose dependencies are satisfied, dispatches them in parallel via provider
//     adapters, and awaits completions using Restate's WaitFirst.
//   - Expression Hydrator: resolves "resources.<name>.outputs.<field>" references
//     just before dispatch, writing typed values back into the resource spec.
//   - Event Pipeline: CloudEvents flow through EventBus (validation + sequencing),
//     EventStore (chunked per-deployment persistence), EventIndex (cross-deployment
//     queries), and SinkRouter (fan-out delivery with retries and circuit breaker).
//   - Retention: workspace-scoped GC prunes old events from stores and the index
//     on a configurable schedule, optionally shipping events to a drain sink
//     before deletion.
package orchestrator

import (
	"encoding/json"
	"time"

	"github.com/shirvan/praxis/pkg/types"
)

const (
	// DeploymentWorkflowServiceName is the public Restate workflow name used for
	// apply and re-apply orchestration runs.
	DeploymentWorkflowServiceName = "DeploymentWorkflow"

	// DeploymentDeleteWorkflowServiceName is the dedicated workflow used for
	// asynchronous delete flows.
	DeploymentDeleteWorkflowServiceName = "DeploymentDeleteWorkflow"

	// DeploymentRollbackWorkflowServiceName is the dedicated workflow used for
	// rollback flows that delete only resources proven ready by the event store.
	DeploymentRollbackWorkflowServiceName = "DeploymentRollbackWorkflow"

	// DeploymentStateServiceName is the Restate Virtual Object that stores the
	// durable per-deployment lifecycle record.
	DeploymentStateServiceName = "DeploymentStateObj"

	// DeploymentIndexServiceName is the fixed-key listing object.
	DeploymentIndexServiceName = "DeploymentIndex"

	// EventBusServiceName is the CloudEvents entrypoint used by producers.
	EventBusServiceName = "EventBus"

	// DeploymentEventStoreServiceName stores CloudEvents in chunked per-deployment streams.
	DeploymentEventStoreServiceName = "DeploymentEventStore"

	// EventIndexServiceName stores cross-deployment event query state.
	EventIndexServiceName = "EventIndex"

	// SinkRouterServiceName fan-outs stored events to configured sinks.
	SinkRouterServiceName = "SinkRouter"

	// NotificationSinkConfigServiceName stores registered notification sinks.
	NotificationSinkConfigServiceName = "NotificationSinkConfig"

	// DeploymentIndexGlobalKey is the well-known key used by DeploymentIndex.
	DeploymentIndexGlobalKey = "global"

	// EventIndexGlobalKey is the fixed key used by the cross-deployment event index.
	EventIndexGlobalKey = "global"

	// EventBusGlobalKey is the fixed key used by the event bus object.
	EventBusGlobalKey = "global"

	// NotificationSinkConfigGlobalKey is the fixed key used by sink configuration.
	NotificationSinkConfigGlobalKey = "global"

	// ResourceIndexServiceName is the Restate Virtual Object for cross-deployment resource queries.
	ResourceIndexServiceName = "ResourceIndex"

	// ResourceIndexGlobalKey is the well-known key used by ResourceIndex.
	ResourceIndexGlobalKey = "global"
)

// DeploymentPlan is the input accepted by DeploymentWorkflow.Run.
//
// The command service builds this after template evaluation, SSM resolution,
// DAG validation, and canonical key construction. By the time the workflow sees
// it, the plan is already a deployment-specific execution contract.
type DeploymentPlan struct {
	// Key is the stable deployment identifier. The DeploymentState virtual object
	// uses this as its key.
	Key string `json:"key"`

	// Account is the resolved AWS account name for all provider operations in
	// this deployment.
	Account string `json:"account,omitempty"`

	// Resources contains the fully rendered resource documents and their
	// dependency metadata.
	Resources []PlanResource `json:"resources"`

	// Variables is the template-level variable map. The dispatch-time expression
	// hydrator receives it alongside collected dependency outputs.
	Variables map[string]any `json:"variables,omitempty"`

	// CreatedAt records when the apply request entered orchestration.
	CreatedAt time.Time `json:"createdAt"`

	// TemplatePath is preserved for operator visibility.
	TemplatePath string `json:"templatePath,omitempty"`

	// Workspace is the workspace name associated with this deployment.
	Workspace string `json:"workspace,omitempty"`

	// ForceReplace lists template-local resource names that should be deleted
	// and re-provisioned regardless of plan diff results.
	ForceReplace []string `json:"forceReplace,omitempty"`

	// AllowReplace, when true, instructs the workflow to automatically
	// replace any resource whose driver returns a 409 immutable-field
	// conflict. The resource is deleted and then re-provisioned.
	// Resources with lifecycle.preventDestroy are still protected.
	AllowReplace bool `json:"allowReplace,omitempty"`

	// MaxParallelism limits the number of concurrent resource operations.
	// Zero means unlimited.
	MaxParallelism int `json:"maxParallelism,omitempty"`

	// RetryConfig is the deployment-wide default retry policy.
	RetryConfig *RetryConfig `json:"retryConfig,omitempty"`
}

// RetryConfig is the deployment-wide default resource retry policy.
type RetryConfig struct {
	MaxRetries int           `json:"maxRetries"`
	BaseDelay  time.Duration `json:"baseDelay"`
	MaxDelay   time.Duration `json:"maxDelay"`
}

// PlanResource is one resource entry inside a deployment plan.
type PlanResource struct {
	// Name is the template-local resource name.
	Name string `json:"name"`

	// Kind selects the typed provider adapter and underlying driver service.
	Kind string `json:"kind"`

	// DriverService is a diagnostic field that records the target driver service
	// name chosen by the command layer.
	DriverService string `json:"driverService"`

	// Key is the canonical Restate object key for the concrete driver instance.
	Key string `json:"key"`

	// Spec is the fully rendered resource document. It may still contain
	// unresolved dispatch-time expressions until dependencies complete.
	Spec json.RawMessage `json:"spec"`

	// Dependencies lists template-local resources that must complete before this
	// resource can be hydrated and dispatched.
	Dependencies []string `json:"dependencies"`

	// Expressions records the exact JSON paths that need dispatch-time
	// hydration from dependency outputs.
	Expressions map[string]string `json:"expressions,omitempty"`

	// Lifecycle holds optional resource-level lifecycle rules parsed from the
	// template. Nil when the template does not declare a lifecycle block.
	Lifecycle *types.LifecyclePolicy `json:"lifecycle,omitempty"`
}

// DeploymentState is the durable per-deployment lifecycle record stored in the
// DeploymentState virtual object.
//
// This type is intentionally workflow-agnostic. Apply and delete workflows both
// read and write it, while the CLI and future AI concierge can query it through
// shared handlers without coupling to workflow internals.
type DeploymentState struct {
	Key          string                    `json:"key"`
	Account      string                    `json:"account,omitempty"`
	Workspace    string                    `json:"workspace,omitempty"`
	Status       types.DeploymentStatus    `json:"status"`
	TemplatePath string                    `json:"templatePath,omitempty"`
	Resources    map[string]*ResourceState `json:"resources"`
	Outputs      map[string]map[string]any `json:"outputs"`
	Error        string                    `json:"error,omitempty"`
	Cancelled    bool                      `json:"cancelled,omitempty"`
	Generation   int64                     `json:"generation"`
	CreatedAt    time.Time                 `json:"createdAt"`
	UpdatedAt    time.Time                 `json:"updatedAt"`
}

// ResourceState is the deployment-scoped view of one resource during
// orchestration.
type ResourceState struct {
	Name          string                         `json:"name"`
	Kind          string                         `json:"kind"`
	DriverService string                         `json:"driverService,omitempty"`
	Key           string                         `json:"key"`
	DependsOn     []string                       `json:"dependsOn,omitempty"`
	Status        types.DeploymentResourceStatus `json:"status"`
	Error         string                         `json:"error,omitempty"`
	Lifecycle     *types.LifecyclePolicy         `json:"lifecycle,omitempty"`
	PriorReady    bool                           `json:"priorReady,omitempty"`
	Conditions    []types.Condition              `json:"conditions,omitempty"`
}

// DeploymentResult is the final workflow output returned from both apply and
// delete workflows.
type DeploymentResult struct {
	Key            string                     `json:"key"`
	Status         types.DeploymentStatus     `json:"status"`
	Resources      []types.DeploymentResource `json:"resources"`
	Outputs        map[string]map[string]any  `json:"outputs"`
	Error          string                     `json:"error,omitempty"`
	ResourceErrors map[string]string          `json:"resourceErrors,omitempty"`
}

// ResourceUpdate updates one resource entry inside DeploymentState.
type ResourceUpdate struct {
	Name       string                         `json:"name"`
	Status     types.DeploymentResourceStatus `json:"status"`
	Error      string                         `json:"error,omitempty"`
	Outputs    map[string]any                 `json:"outputs,omitempty"`
	Conditions []types.Condition              `json:"conditions,omitempty"`
}

// StatusUpdate moves the deployment as a whole into a new lifecycle phase such
// as Running or Deleting.
type StatusUpdate struct {
	Status    types.DeploymentStatus `json:"status"`
	Error     string                 `json:"error,omitempty"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

// FinalizeRequest records a terminal deployment state.
type FinalizeRequest struct {
	Status    types.DeploymentStatus `json:"status"`
	Error     string                 `json:"error,omitempty"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

// MoveResourceRequest renames a resource within a deployment or moves it to
// another deployment. Only allowed when the deployment is in a terminal state
// (Complete, Failed, Cancelled).
type MoveResourceRequest struct {
	// ResourceName is the current template-local resource name.
	ResourceName string `json:"resourceName"`
	// NewName is the new template-local resource name. If empty, the existing
	// name is preserved (useful for cross-deployment moves combined with a
	// destination deployment key on the caller).
	NewName string `json:"newName,omitempty"`
}

// DeleteRequest is the input payload accepted by DeploymentDeleteWorkflow.Run.
type DeleteRequest struct {
	DeploymentKey string `json:"deploymentKey"`

	// Force overrides lifecycle.preventDestroy protection on resources.
	Force bool `json:"force,omitempty"`

	// Orphan leaves resources running in the provider and only removes Praxis
	// management state when supported by lifecycle deletion policies.
	Orphan bool `json:"orphan,omitempty"`

	// Parallelism limits concurrent delete operations. Zero means unlimited.
	Parallelism int `json:"parallelism,omitempty"`
}

// ReconcileAllRequest triggers reconciliation for every resource in a
// deployment.
type ReconcileAllRequest struct {
	Force bool `json:"force,omitempty"`
}

// ReconcileAllResponse reports how many resource reconcile requests were sent.
type ReconcileAllResponse struct {
	Triggered int      `json:"triggered"`
	Skipped   []string `json:"skipped,omitempty"`
}

// RollbackResource identifies a single resource that was successfully provisioned
// (according to the event store) and should be torn down during rollback.
// The Sequence field records the event store sequence number of the last
// resource.ready event, used to sort resources by most-recently-provisioned-first.
type RollbackResource struct {
	Sequence int64  `json:"sequence"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// RollbackPlan lists the resources eligible for rollback deletion, ordered by
// reverse provisioning sequence. The event store builds this by scanning
// resource.ready events and selecting the latest per resource name.
type RollbackPlan struct {
	DeploymentKey string             `json:"deploymentKey"`
	Resources     []RollbackResource `json:"resources,omitempty"`
}

// EventSequenceRange identifies a contiguous range of event sequences within
// one deployment's event store. Used by retention to communicate which chunks
// were deleted so the EventIndex can prune its corresponding entries.
type EventSequenceRange struct {
	DeploymentKey string `json:"deploymentKey"`
	StartSequence int64  `json:"startSequence"`
	EndSequence   int64  `json:"endSequence"`
}

// EventStorePruneRequest controls per-deployment event pruning during retention
// sweeps. Events older than Before or exceeding MaxEvents are pruned. When
// ShipBeforeDelete is true, pruned events are first delivered to DrainSink in
// batches before being removed from the store.
type EventStorePruneRequest struct {
	Before           time.Time `json:"before,omitzero"`
	MaxEvents        int       `json:"maxEvents,omitempty"`
	ShipBeforeDelete bool      `json:"shipBeforeDelete,omitempty"`
	DrainSink        string    `json:"drainSink,omitempty"`
	BatchSize        int       `json:"batchSize,omitempty"`
}

// EventStorePruneResult reports the outcome of a single deployment's event
// store prune operation, including how many events and chunks were removed
// and the sequence ranges that were deleted (fed to EventIndex.Prune).
type EventStorePruneResult struct {
	DeploymentKey   string               `json:"deploymentKey"`
	PrunedEvents    int                  `json:"prunedEvents"`
	PrunedChunks    int                  `json:"prunedChunks"`
	RemainingEvents int64                `json:"remainingEvents"`
	ShippedEvents   int                  `json:"shippedEvents"`
	RemovedRanges   []EventSequenceRange `json:"removedRanges,omitempty"`
}

// EventIndexPruneRequest controls the global event index pruning. It removes
// entries that match the workspace and are older than Before, belong to pruned
// store ranges, or exceed MaxEntries (oldest-first after other filters).
type EventIndexPruneRequest struct {
	Workspace     string               `json:"workspace,omitempty"`
	Before        time.Time            `json:"before,omitzero"`
	MaxEntries    int                  `json:"maxEntries,omitempty"`
	RemovedRanges []EventSequenceRange `json:"removedRanges,omitempty"`
}

// EventIndexPruneResult reports how many entries the global index removed and
// how many remain after pruning.
type EventIndexPruneResult struct {
	Removed   int `json:"removed"`
	Remaining int `json:"remaining"`
}

// DrainBatchRequest sends a batch of events to a specific notification sink.
// Used by the retention system to ship events to a drain sink before deleting
// them from the event store.
type DrainBatchRequest struct {
	SinkName string                `json:"sinkName"`
	Records  []SequencedCloudEvent `json:"records"`
}

// RetentionSweepRequest triggers a workspace-scoped retention sweep. The sweep
// iterates all deployments in the workspace, prunes their event stores according
// to the workspace's retention policy, and then prunes the global event index.
type RetentionSweepRequest struct {
	Workspace string `json:"workspace"`
}

// RetentionSweepResult summarises one complete retention sweep across a workspace.
type RetentionSweepResult struct {
	Workspace          string   `json:"workspace"`
	DeploymentsScanned int      `json:"deploymentsScanned"`
	DeploymentsPruned  int      `json:"deploymentsPruned"`
	PrunedEvents       int      `json:"prunedEvents"`
	PrunedChunks       int      `json:"prunedChunks"`
	ShippedEvents      int      `json:"shippedEvents"`
	IndexEntriesPruned int      `json:"indexEntriesPruned"`
	FailedDeployments  []string `json:"failedDeployments,omitempty"`
}
