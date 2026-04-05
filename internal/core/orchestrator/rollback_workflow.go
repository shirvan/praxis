// rollback_workflow.go implements targeted rollback of a failed deployment.
//
// Unlike a full delete (which tears down every resource), rollback consults the
// event store to determine which resources were actually provisioned successfully
// (have a resource.ready event) and only deletes those. Resources that never
// reached the ready state are left alone. This makes rollback safe for partial
// failures where some resources were never created.
//
// The rollback plan is computed by DeploymentEventStore.RollbackPlan, which
// scans resource.ready events and returns resources sorted by reverse
// provisioning order (most-recently-provisioned first).
package orchestrator

import (
	"fmt"
	"time"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/pkg/types"
)

// rollbackResourceTimeout is the maximum time to wait for a single resource's
// delete sub-invocation during rollback before recording a failure and moving on.
const rollbackResourceTimeout = 5 * time.Minute

// DeploymentRollbackWorkflow runs a targeted rollback that deletes only
// resources proven to have been successfully provisioned, based on the
// event store's record of resource.ready events.
type DeploymentRollbackWorkflow struct {
	// providers resolves resource kinds to typed driver adapters.
	providers *provider.Registry
}

// NewDeploymentRollbackWorkflow constructs the rollback workflow.
func NewDeploymentRollbackWorkflow(providers *provider.Registry) *DeploymentRollbackWorkflow {
	return &DeploymentRollbackWorkflow{providers: providers}
}

// ServiceName returns the Restate service name for the rollback workflow.
func (*DeploymentRollbackWorkflow) ServiceName() string {
	return DeploymentRollbackWorkflowServiceName
}

// Run executes a targeted rollback:
//
//  1. Fetches the current deployment state from DeploymentStateObj.
//  2. Asks the event store for a RollbackPlan: the set of resources that
//     have a resource.ready event (i.e. were successfully provisioned).
//  3. Iterates the plan in reverse provisioning order, deleting each resource.
//  4. Skips resources already in a Deleted state or guarded by preventDestroy.
//  5. Marks the deployment as Deleted (full success) or Failed (partial).
//
// The rollback plan is intentionally conservative: resources that failed during
// apply and never emitted resource.ready are excluded, avoiding spurious delete
// calls against resources that may not exist.
func (w *DeploymentRollbackWorkflow) Run(ctx restate.WorkflowContext, req DeleteRequest) (DeploymentResult, error) {
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

	// Fetch the rollback plan from the event store. The plan's Resources list
	// contains only resources with resource.ready events, sorted by reverse
	// provisioning sequence (most recently provisioned first).
	rollbackPlan, err := restate.Object[RollbackPlan](ctx, DeploymentEventStoreServiceName, req.DeploymentKey, "RollbackPlan").Request(restate.Void{})
	if err != nil {
		return DeploymentResult{}, err
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

	// Seed execution state with the current resource statuses and errors from
	// the deployment state so that skip logic and failure summaries reflect
	// the full picture (including resources that failed during the apply run).
	exec := newExecutionState(planResourcesFromState(state))
	exec.loadOutputs(state.Outputs)
	for name, resource := range state.Resources {
		if resource == nil {
			continue
		}
		exec.statuses[name] = resource.Status
		if resource.Error != "" {
			exec.errors[name] = resource.Error
		}
	}

	// Iterate the rollback plan. Each item is a resource that reached Ready
	// during the apply run and should be deleted to undo the deployment.
	cancellationRequested := false
	for _, item := range rollbackPlan.Resources {
		resource, ok := exec.plan[item.Name]
		if !ok {
			continue
		}
		// Skip resources that are already deleted (e.g. from a prior partial
		// rollback attempt or manual cleanup).
		if current := state.Resources[item.Name]; current == nil || current.Status == types.DeploymentResourceDeleted {
			continue
		}

		// Poll the cancellation flag. Once set, stop dispatching new deletes
		// and finalize as Failed since not all resources were cleaned up.
		if !cancellationRequested {
			cancelled, err := deploymentCancelled(ctx, req.DeploymentKey)
			if err != nil {
				return DeploymentResult{}, err
			}
			if cancelled {
				cancellationRequested = true
			}
		}
		if cancellationRequested {
			exec.markSkipped(item.Name, "skipped: rollback workflow cancelled")
			continue
		}

		if resource.Lifecycle != nil && resource.Lifecycle.PreventDestroy {
			policyEvent, eventErr := NewPolicyPreventedDestroyEvent(req.DeploymentKey, state.Workspace, state.Generation, time.Time{}, item.Name, resource.Kind, "rollback")
			if eventErr != nil {
				return DeploymentResult{}, eventErr
			}
			if err := EmitCloudEvent(ctx, policyEvent); err != nil {
				return DeploymentResult{}, err
			}
			if err := w.recordRollbackFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, item.Name, resource.Kind,
				fmt.Sprintf("resource %s has lifecycle.preventDestroy enabled; refusing to rollback", item.Name)); err != nil {
				return DeploymentResult{}, err
			}
			continue
		}

		adapter, err := w.providers.Get(resource.Kind)
		if err != nil {
			if err := w.recordRollbackFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, item.Name, resource.Kind, err.Error()); err != nil {
				return DeploymentResult{}, err
			}
			continue
		}

		exec.markDeleting(item.Name)
		if err := updateDeploymentResource(ctx, req.DeploymentKey, ResourceUpdate{
			Name:   item.Name,
			Status: types.DeploymentResourceDeleting,
		}); err != nil {
			return DeploymentResult{}, err
		}
		deleteEvent, eventErr := NewResourceDeleteStartedEvent(req.DeploymentKey, state.Workspace, state.Generation, time.Time{}, item.Name, resource.Kind)
		if eventErr != nil {
			return DeploymentResult{}, eventErr
		}
		if err := EmitDeploymentCloudEvent(ctx, deleteEvent); err != nil {
			return DeploymentResult{}, err
		}

		invocation, err := adapter.Delete(ctx, resource.Key)
		if err != nil {
			if err := w.recordRollbackFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, item.Name, resource.Kind, fmt.Sprintf("failed to dispatch rollback delete: %v", err)); err != nil {
				return DeploymentResult{}, err
			}
			continue
		}

		// Await the delete with a timeout to prevent a single resource from
		// blocking the entire rollback workflow.
		timeout := restate.After(ctx, rollbackResourceTimeout)
		first, err := restate.WaitFirst(ctx, invocation.Future(), timeout)
		if err != nil {
			if err := w.recordRollbackFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, item.Name, resource.Kind, fmt.Sprintf("rollback wait error: %v", err)); err != nil {
				return DeploymentResult{}, err
			}
			continue
		}
		if first == timeout {
			if err := w.recordRollbackFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, item.Name, resource.Kind, fmt.Sprintf("rollback delete timed out after %s", rollbackResourceTimeout)); err != nil {
				return DeploymentResult{}, err
			}
			continue
		}
		if err := invocation.Done(); err != nil {
			if err := w.recordRollbackFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, item.Name, resource.Kind, err.Error()); err != nil {
				return DeploymentResult{}, err
			}
			continue
		}

		exec.markDeleted(item.Name)
		if err := updateDeploymentResource(ctx, req.DeploymentKey, ResourceUpdate{
			Name:   item.Name,
			Status: types.DeploymentResourceDeleted,
		}); err != nil {
			return DeploymentResult{}, err
		}
		deletedEvent, eventErr := NewResourceDeletedEvent(req.DeploymentKey, state.Workspace, state.Generation, time.Time{}, item.Name, resource.Kind)
		if eventErr != nil {
			return DeploymentResult{}, eventErr
		}
		if err := EmitDeploymentCloudEvent(ctx, deletedEvent); err != nil {
			return DeploymentResult{}, err
		}
		if err := removeResourceIndex(ctx, req.DeploymentKey, item.Name); err != nil {
			return DeploymentResult{}, err
		}
	}

	finalStatus := types.DeploymentDeleted
	finalError := ""
	if cancellationRequested {
		finalStatus = types.DeploymentFailed
		finalError = "rollback workflow cancelled; not all resources were cleaned up"
	} else if exec.hasFailures() {
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

	if finalStatus == types.DeploymentDeleted {
		if err := removeDeploymentSummary(ctx, req.DeploymentKey); err != nil {
			return DeploymentResult{}, err
		}
		if err := removeResourceIndexByDeployment(ctx, req.DeploymentKey); err != nil {
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

// recordRollbackFailure marks a resource as failed during rollback, emits
// a resource.delete.error event, and skips the resource's dependencies
// (resources it depends on) so they are not deleted while the failed
// resource still references them.
func (w *DeploymentRollbackWorkflow) recordRollbackFailure(
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

	skipped := exec.skipDependencies(resourceName, fmt.Sprintf("skipped because dependent %s failed to rollback-delete", resourceName))
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
