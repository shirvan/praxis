package orchestrator

import (
	"fmt"

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

func NewDeploymentDeleteWorkflow(providers *provider.Registry) *DeploymentDeleteWorkflow {
	if providers == nil {
		providers = provider.NewRegistry()
	}
	return &DeploymentDeleteWorkflow{providers: providers}
}

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
	if err := appendEvent(ctx, req.DeploymentKey, DeploymentEvent{
		DeploymentKey: req.DeploymentKey,
		Status:        types.DeploymentDeleting,
		Message:       "deployment delete workflow started",
	}); err != nil {
		return DeploymentResult{}, err
	}

	exec := newExecutionState(resources)
	exec.loadOutputs(state.Outputs)

	for _, name := range graph.ReverseTopo() {
		if exec.skipped[name] {
			continue
		}

		resource := exec.plan[name]
		adapter, err := w.providers.Get(resource.Kind)
		if err != nil {
			if err := w.recordDeleteFailure(ctx, req.DeploymentKey, exec, name, resource.Kind, err.Error()); err != nil {
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
		if err := appendEvent(ctx, req.DeploymentKey, DeploymentEvent{
			DeploymentKey: req.DeploymentKey,
			Status:        types.DeploymentDeleting,
			ResourceName:  name,
			ResourceKind:  resource.Kind,
			Message:       fmt.Sprintf("deleting %s resource", resource.Kind),
		}); err != nil {
			return DeploymentResult{}, err
		}

		invocation, err := adapter.Delete(ctx, resource.Key)
		if err != nil {
			if err := w.recordDeleteFailure(ctx, req.DeploymentKey, exec, name, resource.Kind, fmt.Sprintf("failed to dispatch delete: %v", err)); err != nil {
				return DeploymentResult{}, err
			}
			continue
		}

		if err := invocation.Done(); err != nil {
			if err := w.recordDeleteFailure(ctx, req.DeploymentKey, exec, name, resource.Kind, err.Error()); err != nil {
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
		if err := appendEvent(ctx, req.DeploymentKey, DeploymentEvent{
			DeploymentKey: req.DeploymentKey,
			Status:        types.DeploymentDeleting,
			ResourceName:  name,
			ResourceKind:  resource.Kind,
			Message:       fmt.Sprintf("resource %s deleted", name),
		}); err != nil {
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
	if err := appendEvent(ctx, req.DeploymentKey, DeploymentEvent{
		DeploymentKey: req.DeploymentKey,
		Status:        finalStatus,
		Message:       fmt.Sprintf("deployment delete finished with status %s", finalStatus),
		Error:         finalError,
	}); err != nil {
		return DeploymentResult{}, err
	}

	return exec.result(req.DeploymentKey, finalStatus, finalError), nil
}

func (w *DeploymentDeleteWorkflow) recordDeleteFailure(
	ctx restate.WorkflowContext,
	deploymentKey string,
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
	if err := appendEvent(ctx, deploymentKey, DeploymentEvent{
		DeploymentKey: deploymentKey,
		Status:        types.DeploymentDeleting,
		ResourceName:  resourceName,
		ResourceKind:  resourceKind,
		Message:       fmt.Sprintf("resource %s failed to delete", resourceName),
		Error:         errMsg,
	}); err != nil {
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
		if err := appendEvent(ctx, deploymentKey, DeploymentEvent{
			DeploymentKey: deploymentKey,
			Status:        types.DeploymentDeleting,
			ResourceName:  name,
			ResourceKind:  resource.Kind,
			Message:       exec.errors[name],
		}); err != nil {
			return err
		}
	}
	return nil
}
