package orchestrator

import (
	"fmt"
	"time"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/pkg/types"
)

type DeploymentRollbackWorkflow struct {
	providers *provider.Registry
}

func NewDeploymentRollbackWorkflow(providers *provider.Registry) *DeploymentRollbackWorkflow {
	return &DeploymentRollbackWorkflow{providers: providers}
}

func (*DeploymentRollbackWorkflow) ServiceName() string {
	return DeploymentRollbackWorkflowServiceName
}

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

	for _, item := range rollbackPlan.Resources {
		resource, ok := exec.plan[item.Name]
		if !ok {
			continue
		}
		if current := state.Resources[item.Name]; current == nil || current.Status == types.DeploymentResourceDeleted {
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
	return EmitDeploymentCloudEvent(ctx, errorEvent)
}
