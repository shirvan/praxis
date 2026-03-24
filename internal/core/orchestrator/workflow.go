package orchestrator

import (
	"fmt"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/dag"
	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/pkg/types"
)

// DeploymentWorkflow executes one apply/re-apply run for a deployment.
//
// The workflow itself is intentionally thin on durable state. The authoritative
// lifecycle record lives in DeploymentStateObj. This keeps the workflow focused
// on scheduling, dispatching, waiting, and translating driver outcomes into
// deployment-level state transitions.
type DeploymentWorkflow struct {
	providers *provider.Registry
}

// NewDeploymentWorkflow constructs the apply workflow.
func NewDeploymentWorkflow(providers *provider.Registry) *DeploymentWorkflow {
	return &DeploymentWorkflow{providers: providers}
}

func (*DeploymentWorkflow) ServiceName() string {
	return DeploymentWorkflowServiceName
}

// Run executes the deployment plan using eager dispatch:
//
//   - resources start as soon as their direct dependencies complete
//   - outputs are fed back into dispatch-time expression hydration
//   - dependency failures mark downstream resources as Skipped
//   - cancellation stops new dispatches but lets in-flight operations finish
func (w *DeploymentWorkflow) Run(ctx restate.WorkflowContext, plan DeploymentPlan) (DeploymentResult, error) {
	if plan.Key == "" {
		return DeploymentResult{}, restate.TerminalError(fmt.Errorf("deployment key is required"), 400)
	}

	state, err := getDeploymentState(ctx, plan.Key)
	if err != nil {
		return DeploymentResult{}, err
	}
	if state == nil {
		return DeploymentResult{}, restate.TerminalError(
			fmt.Errorf("deployment %q must be initialized in %s before starting the workflow", plan.Key, DeploymentStateServiceName),
			404,
		)
	}

	graph, err := graphFromPlanResources(plan.Resources)
	if err != nil {
		now, nowErr := currentTime(ctx)
		if nowErr == nil {
			if finalizeErr := finalizeDeployment(ctx, plan.Key, FinalizeRequest{
				Status:    types.DeploymentFailed,
				Error:     err.Error(),
				UpdatedAt: now,
			}); finalizeErr != nil {
				return DeploymentResult{}, restate.TerminalError(
					fmt.Errorf("invalid deployment graph: %w (additionally, failed to finalize deployment: %v)", err, finalizeErr),
					400,
				)
			}
		}
		return DeploymentResult{}, restate.TerminalError(fmt.Errorf("invalid deployment graph: %w", err), 400)
	}

	now, err := currentTime(ctx)
	if err != nil {
		return DeploymentResult{}, err
	}
	if err := setDeploymentStatus(ctx, plan.Key, StatusUpdate{
		Status:    types.DeploymentRunning,
		UpdatedAt: now,
	}); err != nil {
		return DeploymentResult{}, err
	}
	state.Status = types.DeploymentRunning
	state.UpdatedAt = now
	if err := upsertDeploymentSummary(ctx, deploymentSummaryFromState(state)); err != nil {
		return DeploymentResult{}, err
	}
	if err := appendEvent(ctx, plan.Key, DeploymentEvent{
		DeploymentKey: plan.Key,
		Status:        types.DeploymentRunning,
		Message:       "deployment workflow started",
	}); err != nil {
		return DeploymentResult{}, err
	}

	schedule := dag.NewSchedule(graph)
	exec := newExecutionState(plan.Resources)
	exec.loadOutputs(state.Outputs)

	replaceSet := make(map[string]bool, len(plan.ForceReplace))
	for _, name := range plan.ForceReplace {
		replaceSet[name] = true
	}

	inFlight := make(map[string]provider.ProvisionInvocation)
	cancellationRequested := false

	for {
		if !cancellationRequested {
			cancelled, err := deploymentCancelled(ctx, plan.Key)
			if err != nil {
				return DeploymentResult{}, err
			}
			if cancelled {
				cancellationRequested = true
				if err := appendEvent(ctx, plan.Key, DeploymentEvent{
					DeploymentKey: plan.Key,
					Status:        types.DeploymentRunning,
					Message:       "deployment cancellation requested; draining in-flight resources",
				}); err != nil {
					return DeploymentResult{}, err
				}
			}
		}

		if !cancellationRequested {
			ready := exec.ready(schedule)
			for _, name := range ready {
				resource := exec.plan[name]

				hydratedSpec, err := HydrateExprs(resource.Spec, resource.Expressions, exec.outputs)
				if err != nil {
					if err := w.recordApplyFailure(ctx, plan.Key, exec, schedule, name, resource.Kind, fmt.Sprintf("failed to hydrate spec: %v", err)); err != nil {
						return DeploymentResult{}, err
					}
					continue
				}

				adapter, err := w.providers.Get(resource.Kind)
				if err != nil {
					if err := w.recordApplyFailure(ctx, plan.Key, exec, schedule, name, resource.Kind, err.Error()); err != nil {
						return DeploymentResult{}, err
					}
					continue
				}

				decodedSpec, err := adapter.DecodeSpec(hydratedSpec)
				if err != nil {
					if err := w.recordApplyFailure(ctx, plan.Key, exec, schedule, name, resource.Kind, fmt.Sprintf("failed to decode driver spec: %v", err)); err != nil {
						return DeploymentResult{}, err
					}
					continue
				}

				// Force replacement: delete the existing resource before re-provisioning.
				if replaceSet[name] {
					if resource.Lifecycle != nil && resource.Lifecycle.PreventDestroy {
						if err := w.recordApplyFailure(ctx, plan.Key, exec, schedule, name, resource.Kind,
							fmt.Sprintf("resource %s has lifecycle.preventDestroy enabled; refusing to force-replace", name)); err != nil {
							return DeploymentResult{}, err
						}
						continue
					}

					if err := appendEvent(ctx, plan.Key, DeploymentEvent{
						DeploymentKey: plan.Key,
						Status:        types.DeploymentRunning,
						ResourceName:  name,
						ResourceKind:  resource.Kind,
						Message:       fmt.Sprintf("force-replacing %s: deleting before re-provision", name),
					}); err != nil {
						return DeploymentResult{}, err
					}

					delInvocation, err := adapter.Delete(ctx, resource.Key)
					if err != nil {
						if err := w.recordApplyFailure(ctx, plan.Key, exec, schedule, name, resource.Kind, fmt.Sprintf("force-replace delete dispatch failed: %v", err)); err != nil {
							return DeploymentResult{}, err
						}
						continue
					}
					if err := delInvocation.Done(); err != nil {
						if err := w.recordApplyFailure(ctx, plan.Key, exec, schedule, name, resource.Kind, fmt.Sprintf("force-replace delete failed: %v", err)); err != nil {
							return DeploymentResult{}, err
						}
						continue
					}
				}

				invocation, err := adapter.Provision(ctx, resource.Key, plan.Account, decodedSpec)
				if err != nil {
					if err := w.recordApplyFailure(ctx, plan.Key, exec, schedule, name, resource.Kind, fmt.Sprintf("failed to dispatch driver call: %v", err)); err != nil {
						return DeploymentResult{}, err
					}
					continue
				}

				exec.markProvisioning(name)
				inFlight[name] = invocation
				if err := updateDeploymentResource(ctx, plan.Key, ResourceUpdate{
					Name:   name,
					Status: types.DeploymentResourceProvisioning,
				}); err != nil {
					return DeploymentResult{}, err
				}
				if err := appendEvent(ctx, plan.Key, DeploymentEvent{
					DeploymentKey: plan.Key,
					Status:        types.DeploymentRunning,
					ResourceName:  name,
					ResourceKind:  resource.Kind,
					Message:       fmt.Sprintf("dispatched %s resource", resource.Kind),
				}); err != nil {
					return DeploymentResult{}, err
				}
			}
		}

		if len(inFlight) == 0 {
			if cancellationRequested {
				break
			}
			if len(exec.ready(schedule)) == 0 {
				break
			}
		}

		if len(inFlight) == 0 {
			continue
		}

		future, err := restate.WaitFirst(ctx, provisionFutures(inFlight)...)
		if err != nil {
			return DeploymentResult{}, err
		}

		resourceName, invocation, ok := matchProvisionFuture(inFlight, future)
		if !ok {
			return DeploymentResult{}, fmt.Errorf("completed future did not match any tracked resource")
		}
		delete(inFlight, resourceName)

		outputs, err := invocation.Outputs()
		if err != nil {
			resource := exec.plan[resourceName]
			if err := w.recordApplyFailure(ctx, plan.Key, exec, schedule, resourceName, resource.Kind, err.Error()); err != nil {
				return DeploymentResult{}, err
			}
			continue
		}

		exec.markReady(resourceName, outputs)
		if err := updateDeploymentResource(ctx, plan.Key, ResourceUpdate{
			Name:    resourceName,
			Status:  types.DeploymentResourceReady,
			Outputs: outputs,
		}); err != nil {
			return DeploymentResult{}, err
		}
		resource := exec.plan[resourceName]
		if err := appendEvent(ctx, plan.Key, DeploymentEvent{
			DeploymentKey: plan.Key,
			Status:        types.DeploymentRunning,
			ResourceName:  resourceName,
			ResourceKind:  resource.Kind,
			Message:       fmt.Sprintf("resource %s is ready", resourceName),
		}); err != nil {
			return DeploymentResult{}, err
		}
	}

	finalStatus := types.DeploymentComplete
	finalError := ""
	if cancellationRequested {
		pending := exec.skipPendingForCancellation()
		for _, name := range pending {
			resource := exec.plan[name]
			if err := updateDeploymentResource(ctx, plan.Key, ResourceUpdate{
				Name:   name,
				Status: types.DeploymentResourceSkipped,
				Error:  exec.errors[name],
			}); err != nil {
				return DeploymentResult{}, err
			}
			if err := appendEvent(ctx, plan.Key, DeploymentEvent{
				DeploymentKey: plan.Key,
				Status:        types.DeploymentCancelled,
				ResourceName:  name,
				ResourceKind:  resource.Kind,
				Message:       exec.errors[name],
			}); err != nil {
				return DeploymentResult{}, err
			}
		}
		finalStatus = types.DeploymentCancelled
		finalError = exec.failureSummary()
	} else if exec.hasFailures() {
		finalStatus = types.DeploymentFailed
		finalError = exec.failureSummary()
	}

	now, err = currentTime(ctx)
	if err != nil {
		return DeploymentResult{}, err
	}
	if err := finalizeDeployment(ctx, plan.Key, FinalizeRequest{
		Status:    finalStatus,
		Error:     finalError,
		UpdatedAt: now,
	}); err != nil {
		return DeploymentResult{}, err
	}
	state.Status = finalStatus
	state.Error = finalError
	state.UpdatedAt = now
	state.Outputs = exec.outputs
	if err := upsertDeploymentSummary(ctx, deploymentSummaryFromState(state)); err != nil {
		return DeploymentResult{}, err
	}
	if err := appendEvent(ctx, plan.Key, DeploymentEvent{
		DeploymentKey: plan.Key,
		Status:        finalStatus,
		Message:       fmt.Sprintf("deployment finished with status %s", finalStatus),
		Error:         finalError,
	}); err != nil {
		return DeploymentResult{}, err
	}

	return exec.result(plan.Key, finalStatus, finalError), nil
}

func (w *DeploymentWorkflow) recordApplyFailure(
	ctx restate.WorkflowContext,
	deploymentKey string,
	exec *executionState,
	schedule *dag.Schedule,
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
	if err := appendEvent(ctx, deploymentKey, DeploymentEvent{
		DeploymentKey: deploymentKey,
		Status:        types.DeploymentRunning,
		ResourceName:  resourceName,
		ResourceKind:  resourceKind,
		Message:       fmt.Sprintf("resource %s failed", resourceName),
		Error:         errMsg,
	}); err != nil {
		return err
	}

	skipped := exec.skipAffectedDependents(schedule, resourceName, fmt.Sprintf("skipped because dependency %s failed", resourceName))
	for _, name := range skipped {
		resource := exec.plan[name]
		if err := updateDeploymentResource(ctx, deploymentKey, ResourceUpdate{
			Name:   name,
			Status: types.DeploymentResourceSkipped,
			Error:  exec.errors[name],
		}); err != nil {
			return err
		}
		if err := appendEvent(ctx, deploymentKey, DeploymentEvent{
			DeploymentKey: deploymentKey,
			Status:        types.DeploymentRunning,
			ResourceName:  name,
			ResourceKind:  resource.Kind,
			Message:       exec.errors[name],
		}); err != nil {
			return err
		}
	}
	return nil
}

func provisionFutures(inFlight map[string]provider.ProvisionInvocation) []restate.Future {
	futures := make([]restate.Future, 0, len(inFlight))
	for _, invocation := range inFlight {
		futures = append(futures, invocation.Future())
	}
	return futures
}

func matchProvisionFuture(inFlight map[string]provider.ProvisionInvocation, future restate.Future) (string, provider.ProvisionInvocation, bool) {
	for name, invocation := range inFlight {
		if invocation.Future() == future {
			return name, invocation, true
		}
	}
	return "", nil, false
}
