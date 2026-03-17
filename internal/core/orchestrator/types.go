package orchestrator

import (
	"encoding/json"
	"time"

	"github.com/praxiscloud/praxis/pkg/types"
)

const (
	// DeploymentWorkflowServiceName is the public Restate workflow name used for
	// apply and re-apply orchestration runs.
	DeploymentWorkflowServiceName = "DeploymentWorkflow"

	// DeploymentDeleteWorkflowServiceName is the dedicated workflow used for
	// asynchronous delete flows.
	DeploymentDeleteWorkflowServiceName = "DeploymentDeleteWorkflow"

	// DeploymentStateServiceName is the Restate Virtual Object that stores the
	// durable per-deployment lifecycle record.
	DeploymentStateServiceName = "DeploymentStateObj"

	// DeploymentIndexServiceName is the fixed-key listing object.
	DeploymentIndexServiceName = "DeploymentIndex"

	// DeploymentEventsServiceName stores append-only deployment progress events.
	DeploymentEventsServiceName = "DeploymentEvents"

	// DeploymentIndexGlobalKey is the well-known key used by DeploymentIndex.
	DeploymentIndexGlobalKey = "global"
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

	// Variables is the template-level variable map. The dispatch-time CEL
	// hydrator receives it alongside collected dependency outputs.
	Variables map[string]any `json:"variables,omitempty"`

	// CreatedAt records when the apply request entered orchestration.
	CreatedAt time.Time `json:"createdAt"`

	// TemplatePath is preserved for operator visibility.
	TemplatePath string `json:"templatePath,omitempty"`
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
	// unresolved dispatch-time CEL expressions until dependencies complete.
	Spec json.RawMessage `json:"spec"`

	// Dependencies lists template-local resources that must complete before this
	// resource can be hydrated and dispatched.
	Dependencies []string `json:"dependencies"`

	// CELExpressions records the exact JSON paths that need dispatch-time CEL
	// hydration from dependency outputs.
	CELExpressions map[string]string `json:"celExpressions,omitempty"`
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
	Name      string                         `json:"name"`
	Kind      string                         `json:"kind"`
	Key       string                         `json:"key"`
	DependsOn []string                       `json:"dependsOn,omitempty"`
	Status    types.DeploymentResourceStatus `json:"status"`
	Error     string                         `json:"error,omitempty"`
}

// DeploymentResult is the final workflow output returned from both apply and
// delete workflows.
type DeploymentResult struct {
	Key       string                     `json:"key"`
	Status    types.DeploymentStatus     `json:"status"`
	Resources []types.DeploymentResource `json:"resources"`
	Outputs   map[string]map[string]any  `json:"outputs"`
	Error     string                     `json:"error,omitempty"`
}

// ResourceUpdate updates one resource entry inside DeploymentState.
type ResourceUpdate struct {
	Name    string                         `json:"name"`
	Status  types.DeploymentResourceStatus `json:"status"`
	Error   string                         `json:"error,omitempty"`
	Outputs map[string]any                 `json:"outputs,omitempty"`
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

// DeleteRequest is the input payload accepted by DeploymentDeleteWorkflow.Run.
type DeleteRequest struct {
	DeploymentKey string `json:"deploymentKey"`
}

// DeploymentEvent is the append-only progress/event feed for one deployment.
//
// The fields are intentionally generic so both humans and agents can consume the
// stream without depending on driver-specific details.
type DeploymentEvent struct {
	Sequence      int64                  `json:"sequence"`
	DeploymentKey string                 `json:"deploymentKey"`
	Status        types.DeploymentStatus `json:"status,omitempty"`
	ResourceName  string                 `json:"resourceName,omitempty"`
	ResourceKind  string                 `json:"resourceKind,omitempty"`
	Message       string                 `json:"message"`
	Error         string                 `json:"error,omitempty"`
	CreatedAt     time.Time              `json:"createdAt"`
}
