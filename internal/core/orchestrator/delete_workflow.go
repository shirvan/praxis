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

	"github.com/shirvan/praxis/internal/core/dag"
	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/pkg/types"
)

// applyDrainInterval is the poll cadence used while waiting for an in-progress
// apply workflow to reach a terminal state before deletion begins.
const applyDrainInterval = 2 * time.Second

// minApplyDrainBudget is the floor for how long the delete workflow waits for
// a running apply to settle before presuming it was hard-killed (e.g. via the
// Restate admin API) and force-transitioning the deployment to Cancelled.
const minApplyDrainBudget = time.Minute

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

// applyDrainBudget returns how long Run waits for an in-progress apply to
// settle: the largest per-resource provision timeout (in-flight driver calls
// keep running after RequestCancel) plus one drain interval of margin, never
// less than minApplyDrainBudget.
func (w *DeploymentDeleteWorkflow) applyDrainBudget(state *DeploymentState) time.Duration {
	budget := minApplyDrainBudget
	resources := planResourcesFromState(state)
	for i := range resources {
		resource := &resources[i]
		adapter, err := w.providers.Get(resource.Kind)
		if err != nil {
			continue
		}
		for _, isUpdate := range []bool{false, true} {
			timeout, err := resolveProvisionTimeout(adapter, resource.Lifecycle, isUpdate)
			if err == nil && timeout > budget {
				budget = timeout
			}
		}
	}
	return budget + applyDrainInterval
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

	// If an apply workflow is still running, wait for it to settle before
	// beginning teardown. The DeleteDeployment handler sends RequestCancel
	// before dispatching us, so the apply should already be draining. We
	// poll until the deployment reaches a terminal or deletable state to
	// avoid concurrent mutation of resources.
	//
	// A hard-kill of the apply workflow via the Restate admin API leaves the
	// state permanently at Running or Pending because the workflow exits before
	// it can call finalizeDeployment. After the drain budget elapses we assume
	// that scenario, force-transition the state to Cancelled, and proceed.
	// This allows operators to flush stuck deployments by deleting them.
	//
	// The budget covers the largest per-resource provision timeout: after
	// RequestCancel the apply stops dispatching new work, but deliberately
	// lets in-flight driver operations finish, which can take minutes for
	// slow resources (RDS, ACM). A 60s heuristic would start deleting
	// resources whose Provision is still executing.
	drainMaxPolls := int(w.applyDrainBudget(state) / applyDrainInterval)
	drainPolls := 0
	for state.Status == types.DeploymentRunning || state.Status == types.DeploymentPending {
		if drainPolls >= drainMaxPolls {
			// Apply workflow is presumed dead. Force the state out of the
			// transient status so the delete can proceed safely.
			now, nowErr := currentTime(ctx)
			if nowErr != nil {
				return DeploymentResult{}, nowErr
			}
			if err := setDeploymentStatus(ctx, req.DeploymentKey, StatusUpdate{
				Status:    types.DeploymentCancelled,
				UpdatedAt: now,
			}); err != nil {
				return DeploymentResult{}, err
			}
			state.Status = types.DeploymentCancelled
			break
		}
		if err := restate.Sleep(ctx, applyDrainInterval); err != nil {
			return DeploymentResult{}, err
		}
		drainPolls++
		state, err = getDeploymentState(ctx, req.DeploymentKey)
		if err != nil {
			return DeploymentResult{}, err
		}
		if state == nil {
			return DeploymentResult{}, restate.TerminalError(fmt.Errorf("deployment %q disappeared while waiting for apply to finish", req.DeploymentKey), 404)
		}
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
	EmitDeploymentCloudEventBestEffort(ctx, startedEvent)

	exec := newExecutionState(resources)
	exec.loadOutputs(state.Outputs)
	schedule := dag.NewSchedule(graph)

	cancellationRequested := false
	inFlight := make(map[string]*inFlightDelete)
	for {

		// Poll the cancellation flag. Once set, stop dispatching new deletes
		// but honour resources already started. A cancelled delete finalizes
		// as Failed (not Deleted) because not all resources were cleaned up.
		if !cancellationRequested {
			cancelled, err := deploymentCancelled(ctx, req.DeploymentKey)
			if err != nil {
				return DeploymentResult{}, err
			}
			if cancelled {
				cancellationRequested = true
			}
		}

		loopNow, err := currentTime(ctx)
		if err != nil {
			return DeploymentResult{}, err
		}

		if !cancellationRequested {
			ready := schedule.ReadyForDelete(completedForDelete(exec), exec.dispatched)
			for _, name := range ready {
				if req.Parallelism > 0 && len(inFlight) >= req.Parallelism {
					break
				}
				if exec.skipped[name] {
					continue
				}

				// Skip resources that were never successfully provisioned or are
				// already deleted/orphaned.
				if rs := state.Resources[name]; rs != nil {
					if shouldSkipDeleteByStatus(rs.Status) {
						exec.markDeleted(name)
						continue
					}
				}

				resource := exec.plan[name]

				deletionPolicy := types.DeletionPolicyDelete
				if req.Orphan {
					deletionPolicy = types.DeletionPolicyOrphan
				} else if resource.Lifecycle != nil && resource.Lifecycle.DeletionPolicy != "" {
					deletionPolicy = resource.Lifecycle.DeletionPolicy
				}
				if deletionPolicy == types.DeletionPolicyOrphan {
					conditions := orphanedConditions(exec.conditionsFor(name), loopNow, "resource orphaned by deletion policy")
					exec.markOrphaned(name)
					exec.setConditions(name, conditions)
					if err := updateDeploymentResource(ctx, req.DeploymentKey, ResourceUpdate{
						Name:       name,
						Status:     types.DeploymentResourceOrphaned,
						Conditions: conditions,
					}); err != nil {
						return DeploymentResult{}, err
					}
					if err := removeResourceIndex(ctx, req.DeploymentKey, name); err != nil {
						return DeploymentResult{}, err
					}
					continue
				}

				// Respect lifecycle.preventDestroy: if the template declared this
				// resource as undestroyable, emit a policy event and mark it failed.
				if resource.Lifecycle != nil && resource.Lifecycle.PreventDestroy {
					if !req.Force {
						policyEvent, eventErr := NewPolicyPreventedDestroyEvent(req.DeploymentKey, state.Workspace, state.Generation, time.Time{}, name, resource.Kind, "delete")
						if eventErr != nil {
							return DeploymentResult{}, eventErr
						}
						EmitCloudEventBestEffort(ctx, policyEvent)
						if err := w.recordDeleteFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, name, resource.Kind,
							fmt.Sprintf("resource %s has lifecycle.preventDestroy enabled; refusing to delete", name), false); err != nil {
							return DeploymentResult{}, err
						}
						continue
					}
					overrideEvent, eventErr := NewForceDeleteOverrideEvent(req.DeploymentKey, state.Workspace, state.Generation, time.Time{}, name, resource.Kind, "delete")
					if eventErr != nil {
						return DeploymentResult{}, eventErr
					}
					EmitCloudEventBestEffort(ctx, overrideEvent)
				}

				adapter, err := w.providers.Get(resource.Kind)
				if err != nil {
					if err := w.recordDeleteFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, name, resource.Kind, err.Error(), req.Force); err != nil {
						return DeploymentResult{}, err
					}
					continue
				}

				deleteTimeout, err := resolveDeleteTimeout(adapter, resource.Lifecycle)
				if err != nil {
					if err := w.recordDeleteFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, name, resource.Kind, err.Error(), req.Force); err != nil {
						return DeploymentResult{}, err
					}
					continue
				}

				exec.markDeleting(name)
				conditions := deletingConditions(exec.conditionsFor(name), loopNow, "resource delete dispatched")
				exec.setConditions(name, conditions)
				if err := updateDeploymentResource(ctx, req.DeploymentKey, ResourceUpdate{
					Name:       name,
					Status:     types.DeploymentResourceDeleting,
					Conditions: conditions,
				}); err != nil {
					return DeploymentResult{}, err
				}
				deleteEvent, eventErr := NewResourceDeleteStartedEvent(req.DeploymentKey, state.Workspace, state.Generation, time.Time{}, name, resource.Kind)
				if eventErr != nil {
					return DeploymentResult{}, eventErr
				}
				EmitDeploymentCloudEventBestEffort(ctx, deleteEvent)

				invocation, err := adapter.Delete(ctx, resource.Key)
				if err != nil {
					if err := w.recordDeleteFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, name, resource.Kind, fmt.Sprintf("failed to dispatch delete: %v", err), req.Force); err != nil {
						return DeploymentResult{}, err
					}
					continue
				}

				inFlight[name] = &inFlightDelete{
					invocation: invocation,
					adapter:    adapter,
					timeout:    deleteTimeout,
				}
			}
		}

		if len(inFlight) == 0 {
			if cancellationRequested {
				break
			}
			if len(schedule.ReadyForDelete(completedForDelete(exec), exec.dispatched)) == 0 {
				break
			}
			continue
		}

		resourceName, pending := nextInFlightDeleteCompletion(inFlight)
		delete(inFlight, resourceName)
		timeout := restate.After(ctx, pending.timeout)
		first, err := restate.WaitFirst(ctx, pending.invocation.Future(), timeout)
		if err != nil {
			resource := exec.plan[resourceName]
			if err := w.recordDeleteFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, resourceName, resource.Kind, fmt.Sprintf("delete wait error: %v", err), req.Force); err != nil {
				return DeploymentResult{}, err
			}
			continue
		}
		if first == timeout {
			resource := exec.plan[resourceName]
			errMsg := timeoutOutcomeMessage("resource delete", pending.timeout)
			if err := w.recordDeleteFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, resourceName, resource.Kind, errMsg, req.Force); err != nil {
				return DeploymentResult{}, err
			}
			if err := recordTimeoutEvidence(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, resourceName, resource.Kind, "resource delete", pending.timeout, errMsg); err != nil {
				return DeploymentResult{}, err
			}
			continue
		}
		if err := pending.invocation.Done(); err != nil {
			resource := exec.plan[resourceName]
			if err := w.recordDeleteFailure(ctx, req.DeploymentKey, state.Workspace, state.Generation, exec, resourceName, resource.Kind, err.Error(), req.Force); err != nil {
				return DeploymentResult{}, err
			}
			continue
		}
		resource := exec.plan[resourceName]

		// Fresh timestamp: loopNow was journaled before a potentially
		// minutes-long delete wait.
		deletedAt, err := currentTime(ctx)
		if err != nil {
			return DeploymentResult{}, err
		}
		conditions := deletedConditions(exec.conditionsFor(resourceName), deletedAt, "resource deleted")
		exec.markDeleted(resourceName)
		exec.setConditions(resourceName, conditions)
		if err := updateDeploymentResource(ctx, req.DeploymentKey, ResourceUpdate{
			Name:       resourceName,
			Status:     types.DeploymentResourceDeleted,
			Conditions: conditions,
		}); err != nil {
			return DeploymentResult{}, err
		}
		deletedEvent, eventErr := NewResourceDeletedEvent(req.DeploymentKey, state.Workspace, state.Generation, time.Time{}, resourceName, resource.Kind)
		if eventErr != nil {
			return DeploymentResult{}, eventErr
		}
		EmitDeploymentCloudEventBestEffort(ctx, deletedEvent)
		if err := removeResourceIndex(ctx, req.DeploymentKey, resourceName); err != nil {
			return DeploymentResult{}, err
		}
	}

	if cancellationRequested {
		now, err := currentTime(ctx)
		if err != nil {
			return DeploymentResult{}, err
		}
		skipped := exec.skipPendingForCancellation()
		for _, name := range skipped {
			conditions := skippedConditions(exec.conditionsFor(name), now, exec.errors[name])
			exec.setConditions(name, conditions)
			if err := updateDeploymentResource(ctx, req.DeploymentKey, ResourceUpdate{
				Name:       name,
				Status:     types.DeploymentResourceSkipped,
				Error:      exec.errors[name],
				Conditions: conditions,
			}); err != nil {
				return DeploymentResult{}, err
			}
		}
	}

	finalStatus := types.DeploymentDeleted
	finalError := ""
	if cancellationRequested {
		finalStatus = types.DeploymentFailed
		finalError = "delete workflow cancelled; not all resources were cleaned up"
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

	// On full success, remove this deployment from the global listing.
	// On partial failure, keep it so operators can see the failed state.
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
	// Terminal events are load-bearing: `praxis observe` exits only when it
	// reads one from the event store. Must persist, not best-effort.
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
//
// When force is true, dependency skipping is bypassed: every resource is
// attempted for deletion regardless of upstream failures.
func (w *DeploymentDeleteWorkflow) recordDeleteFailure(
	ctx restate.WorkflowContext,
	deploymentKey string,
	workspace string,
	generation int64,
	exec *executionState,
	resourceName string,
	resourceKind string,
	errMsg string,
	force bool,
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
	EmitDeploymentCloudEventBestEffort(ctx, errorEvent)

	// When force is set, do not skip dependencies — attempt to delete every
	// resource regardless of upstream failures.
	if force {
		return nil
	}

	skipped := exec.skipDependencies(resourceName, fmt.Sprintf("skipped because dependent %s failed to delete", resourceName))
	for _, name := range skipped {
		resource := exec.plan[name]
		now, err := currentTime(ctx)
		if err != nil {
			return err
		}
		conditions := skippedConditions(exec.conditionsFor(name), now, exec.errors[name])
		exec.setConditions(name, conditions)
		if err := updateDeploymentResource(ctx, deploymentKey, ResourceUpdate{
			Name:       name,
			Status:     types.DeploymentResourceSkipped,
			Error:      exec.errors[name],
			Conditions: conditions,
		}); err != nil {
			return err
		}
		skippedEvent, eventErr := NewResourceSkippedEvent(deploymentKey, workspace, generation, time.Time{}, name, resource.Kind, types.DeploymentDeleting, exec.errors[name])
		if eventErr != nil {
			return eventErr
		}
		EmitDeploymentCloudEventBestEffort(ctx, skippedEvent)
	}
	return nil
}

func completedForDelete(exec *executionState) map[string]bool {
	completed := make(map[string]bool, len(exec.completed)+len(exec.skipped))
	for name, done := range exec.completed {
		if done {
			completed[name] = true
		}
	}
	for name, skipped := range exec.skipped {
		if skipped {
			completed[name] = true
		}
	}
	return completed
}

// shouldSkipDeleteByStatus returns true for resource statuses that indicate
// the resource was never successfully provisioned or is already deleted.
// These resources should be skipped during the delete workflow to avoid
// spurious driver calls against resources that don't exist in the cloud.
//
// Note: Skipped resources are NOT included here. A resource in Skipped state
// was never actually deleted (it was bypassed due to a dependency failure) and
// must be retried on subsequent delete attempts.
func shouldSkipDeleteByStatus(status types.DeploymentResourceStatus) bool {
	switch status {
	case types.DeploymentResourcePending, types.DeploymentResourceDeleted:
		return true
	case types.DeploymentResourceOrphaned:
		return true
	default:
		return false
	}
}
