// delete_workflow.go implements the deployment-wide delete workflow.
//
// Unlike the apply workflow which dispatches resources in forward topological
// order (dependencies first), the delete workflow destroys resources in reverse
// topological order (dependents first). This ensures a resource is not deleted
// while something that references it is still alive.
//
// Delete is a separate Restate Workflow (not a method on the apply workflow)
// so that each operation gets its own durable execution and can be independently
// retried, cancelled, and observed.
package orchestrator

import (
	"fmt"
	"time"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/pkg/types"
)

// DeploymentDeleteWorkflow runs one asynchronous deployment-wide delete flow.
//
// Delete stays separate from apply so both operations remain durable, observable
// workflow-class operations while still sharing the same DeploymentState object.
type DeploymentDeleteWorkflow struct {
	providers *provider.Registry
}

// NewDeploymentDeleteWorkflow constructs the delete workflow with the provider
// registry used to resolve driver adapters for each resource kind.
func NewDeploymentDeleteWorkflow(providers *provider.Registry) *DeploymentDeleteWorkflow {
	return &DeploymentDeleteWorkflow{providers: providers}
}

// ServiceName returns the Restate service name for the delete workflow.
func (*DeploymentDeleteWorkflow) ServiceName() string {
	return DeploymentDeleteWorkflowServiceName
}

// Run deletes resources in reverse topological order.
//
// If deleting one resource fails, its dependencies are marked Skipped because
// they may still be referenced by the failed dependent. Independent branches can
// still continue because the skip set is computed from dependency closure rather
// than by halting the whole workflow immediately.
func (w *DeploymentDeleteWorkflow) Run(ctx restate.WorkflowContext, req DeleteRequest) (DeploymentResult, error) {
	if req.DeploymentKey == "" {
		return DeploymentResult{}, restate.TerminalError(fmt.Errorf("deployment key is required"), 400)
	}

	state, err := getDeploymentState(ctx, req.DeploymentKey)
	if err != nil {
		return DeploymentResult{}, err
	}
	if state == nil {
		return DeploymentResult{}, restate.TerminalError(fmt.Errorf("deployment %q not found", req.DeploymentKey), 404)
	}

	// Reconstruct the DAG from the stored deployment resources so we can
	// determine the reverse topological order for safe deletion.
	resources := planResourcesFromState(state)
	graph, err := graphFromPlanResources(resources)
	if err != nil {
		return DeploymentResult{}, restate.TerminalError(fmt.Errorf("invalid stored deployment graph: %w", err), 500)
	}

	now, err := currentTime(ctx)
	if err != nil {
		return DeploymentResult{}, err
	}
	if err := setDeploymentStatus(ctx, req.DeploymentKey, StatusUpdate{
		Status:    types.DeploymentDeleting,
		UpdatedAt: now,
	}); err != nil {
		return DeploymentResult{}, err
	}
	state.Status = types.DeploymentDeleting
	state.UpdatedAt = now
	if err := upsertDeploymentSummary(ctx, deploymentSummaryFromState(state)); err != nil {
		return DeploymentResult{}, err
	}
	startedEvent, err := NewDeploymentDeleteStartedEvent(req.DeploymentKey, state.Workspace, state.Generation, now)
	if err != nil {
		return DeploymentResult{}, err
	}
	if err := EmitDeploymentCloudEvent(ctx, startedEvent); err != nil {
		return DeploymentResult{}, err
	}

	exec := newExecutionState(resources)
	exec.loadOutputs(state.Outputs)

	// Walk resources in reverse topological order: dependents before their
	// dependencies. This guarantees that if resource B depends on resource A,
	// B is deleted before A. If B's deletion fails, A (and its other deps)
	// are skipped because B may still hold a live reference to A.
	for _, name := range graph.ReverseTopo() {
		if exec.skipped[name] {
			continue
		}

		resource := exec.plan[name]

		// Respect lifecycle.preventDestroy: if the template declared this
		// resource as undestroyable, emit a policy event and mark it failed.
		if resource.Lifecycle != nil && resource.Lifecycle.PreventDestroy {
			policyEvent, eventErr := NewPolicyPreventedDestroyEvent(req.DeploymentKey, state.Workspace, state.Generation, time.Time{}, name, resource.Kind, "delete")
			if eventErr != nil {
				return DeploymentResult{}, eventErr
			}
			if err := EmitCloudEvent(ctx, policyEvent); err != nil {
				return DeploymentResult{}, err
			}
			if err := w.recordDeleteFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, name, resource.Kind,
				fmt.Sprintf("resource %s has lifecycle.preventDestroy enabled; refusing to delete", name)); err != nil {
				return DeploymentResult{}, err
			}
			continue
		}

		adapter, err := w.providers.Get(resource.Kind)
		if err != nil {
			if err := w.recordDeleteFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, name, resource.Kind, err.Error()); err != nil {
				return DeploymentResult{}, err
			}
			continue
		}

		exec.markDeleting(name)
		if err := updateDeploymentResource(ctx, req.DeploymentKey, ResourceUpdate{
			Name:   name,
			Status: types.DeploymentResourceDeleting,
		}); err != nil {
			return DeploymentResult{}, err
		}
		deleteEvent, eventErr := NewResourceDeleteStartedEvent(req.DeploymentKey, state.Workspace, state.Generation, time.Time{}, name, resource.Kind)
		if eventErr != nil {
			return DeploymentResult{}, eventErr
		}
		if err := EmitDeploymentCloudEvent(ctx, deleteEvent); err != nil {
			return DeploymentResult{}, err
		}

		invocation, err := adapter.Delete(ctx, resource.Key)
		if err != nil {
			if err := w.recordDeleteFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, name, resource.Kind, fmt.Sprintf("failed to dispatch delete: %v", err)); err != nil {
				return DeploymentResult{}, err
			}
			continue
		}

		if err := invocation.Done(); err != nil {
			if err := w.recordDeleteFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, name, resource.Kind, err.Error()); err != nil {
				return DeploymentResult{}, err
			}
			continue
		}

		exec.markDeleted(name)
		if err := updateDeploymentResource(ctx, req.DeploymentKey, ResourceUpdate{
			Name:   name,
			Status: types.DeploymentResourceDeleted,
		}); err != nil {
			return DeploymentResult{}, err
		}
		deletedEvent, eventErr := NewResourceDeletedEvent(req.DeploymentKey, state.Workspace, state.Generation, time.Time{}, name, resource.Kind)
		if eventErr != nil {
			return DeploymentResult{}, eventErr
		}
		if err := EmitDeploymentCloudEvent(ctx, deletedEvent); err != nil {
			return DeploymentResult{}, err
		}
	}

	finalStatus := types.DeploymentDeleted
	finalError := ""
	if exec.hasFailures() {
		finalStatus = types.DeploymentFailed
		finalError = exec.failureSummary()
	}

	now, err = currentTime(ctx)
	if err != nil {
		return DeploymentResult{}, err
	}
	if err := finalizeDeployment(ctx, req.DeploymentKey, FinalizeRequest{
		Status:    finalStatus,
		Error:     finalError,
		UpdatedAt: now,
	}); err != nil {
		return DeploymentResult{}, err
	}
	state.Status = finalStatus
	state.Error = finalError
	state.UpdatedAt = now

	// On full success, remove this deployment from the global listing.
	// On partial failure, keep it so operators can see the failed state.
	if finalStatus == types.DeploymentDeleted {
		if err := removeDeploymentSummary(ctx, req.DeploymentKey); err != nil {
			return DeploymentResult{}, err
		}
	} else {
		if err := upsertDeploymentSummary(ctx, deploymentSummaryFromState(state)); err != nil {
			return DeploymentResult{}, err
		}
	}
	terminalEvent, err := NewDeploymentDeleteTerminalEvent(req.DeploymentKey, state.Workspace, state.Generation, now, finalStatus, finalError)
	if err != nil {
		return DeploymentResult{}, err
	}
	if err := EmitDeploymentCloudEvent(ctx, terminalEvent); err != nil {
		return DeploymentResult{}, err
	}

	return exec.result(req.DeploymentKey, finalStatus, finalError), nil
}

// recordDeleteFailure handles a resource-level failure during deletion.
// It marks the resource as Error, emits a resource.delete.error event, and then
// skips the resource's direct dependencies (not dependents). During deletion,
// if a dependent fails to delete, its *dependencies* are skipped because they
// may still be referenced by the failed (still-existing) dependent.
func (w *DeploymentDeleteWorkflow) recordDeleteFailure(
	ctx restate.WorkflowContext,
	deploymentKey string,
	workspace string,
	generation int64,
	exec *executionState,
	resourceName string,
	resourceKind string,
	errMsg string,
) error {
	exec.markFailed(resourceName, errMsg)
	if err := updateDeploymentResource(ctx, deploymentKey, ResourceUpdate{
		Name:   resourceName,
		Status: types.DeploymentResourceError,
		Error:  errMsg,
	}); err != nil {
		return err
	}
	errorEvent, eventErr := NewResourceDeleteErrorEvent(deploymentKey, workspace, generation, time.Time{}, resourceName, resourceKind, errMsg)
	if eventErr != nil {
		return eventErr
	}
	if err := EmitDeploymentCloudEvent(ctx, errorEvent); err != nil {
		return err
	}

	skipped := exec.skipDependencies(resourceName, fmt.Sprintf("skipped because dependent %s failed to delete", resourceName))
	for _, name := range skipped {
		resource := exec.plan[name]
		if err := updateDeploymentResource(ctx, deploymentKey, ResourceUpdate{
			Name:   name,
			Status: types.DeploymentResourceSkipped,
			Error:  exec.errors[name],
		}); err != nil {
			return err
		}
		skippedEvent, eventErr := NewResourceSkippedEvent(deploymentKey, workspace, generation, time.Time{}, name, resource.Kind, types.DeploymentDeleting, exec.errors[name])
		if eventErr != nil {
			return eventErr
		}
		if err := EmitDeploymentCloudEvent(ctx, skippedEvent); err != nil {
			return err
		}
	}
	return nil
}
