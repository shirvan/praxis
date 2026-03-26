package command

import (
	"fmt"
	"strings"
	"time"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/pkg/types"
)

// DeleteDeployment validates a deployment delete request and hands the actual
// reverse-order deletion work to the dedicated delete workflow.
func (s *PraxisCommandService) DeleteDeployment(ctx restate.Context, req DeleteDeploymentRequest) (DeleteDeploymentResponse, error) {
	deploymentKey := strings.TrimSpace(req.DeploymentKey)
	if deploymentKey == "" {
		return DeleteDeploymentResponse{}, restate.TerminalError(fmt.Errorf("deployment key is required"), 400)
	}

	state, err := restate.Object[*orchestrator.DeploymentState](ctx, orchestrator.DeploymentStateServiceName, deploymentKey, "GetState").Request(restate.Void{})
	if err != nil {
		return DeleteDeploymentResponse{}, err
	}
	if state == nil {
		return DeleteDeploymentResponse{}, restate.TerminalError(fmt.Errorf("deployment %q not found", deploymentKey), 404)
	}
	if state.Status == types.DeploymentDeleting {
		return DeleteDeploymentResponse{DeploymentKey: deploymentKey, Status: types.DeploymentDeleting}, nil
	}
	if state.Status == types.DeploymentDeleted {
		return DeleteDeploymentResponse{}, restate.TerminalError(fmt.Errorf("deployment %q is already deleted", deploymentKey), 409)
	}

	commandEvent, err := orchestrator.NewCommandDeleteEvent(deploymentKey, state.Workspace, state.Generation, time.Time{})
	if err != nil {
		return DeleteDeploymentResponse{}, err
	}
	if err := orchestrator.EmitCloudEvent(ctx, commandEvent); err != nil {
		return DeleteDeploymentResponse{}, err
	}

	workflowID := deploymentKey + "-delete"
	restate.WorkflowSend(ctx, orchestrator.DeploymentDeleteWorkflowServiceName, workflowID, "Run").Send(
		orchestrator.DeleteRequest{DeploymentKey: deploymentKey},
		restate.WithIdempotencyKey(workflowID),
	)

	return DeleteDeploymentResponse{DeploymentKey: deploymentKey, Status: types.DeploymentDeleting}, nil
}

func (s *PraxisCommandService) RollbackDeployment(ctx restate.Context, req DeleteDeploymentRequest) (DeleteDeploymentResponse, error) {
	deploymentKey := strings.TrimSpace(req.DeploymentKey)
	if deploymentKey == "" {
		return DeleteDeploymentResponse{}, restate.TerminalError(fmt.Errorf("deployment key is required"), 400)
	}

	state, err := restate.Object[*orchestrator.DeploymentState](ctx, orchestrator.DeploymentStateServiceName, deploymentKey, "GetState").Request(restate.Void{})
	if err != nil {
		return DeleteDeploymentResponse{}, err
	}
	if state == nil {
		return DeleteDeploymentResponse{}, restate.TerminalError(fmt.Errorf("deployment %q not found", deploymentKey), 404)
	}
	if state.Status == types.DeploymentDeleting {
		return DeleteDeploymentResponse{DeploymentKey: deploymentKey, Status: types.DeploymentDeleting}, nil
	}
	if state.Status == types.DeploymentDeleted {
		return DeleteDeploymentResponse{}, restate.TerminalError(fmt.Errorf("deployment %q is already deleted", deploymentKey), 409)
	}
	if state.Status != types.DeploymentFailed && state.Status != types.DeploymentCancelled {
		return DeleteDeploymentResponse{}, restate.TerminalError(fmt.Errorf("deployment %q is %s; rollback is only allowed from Failed or Cancelled", deploymentKey, state.Status), 409)
	}

	commandEvent, err := orchestrator.NewCommandDeleteEvent(deploymentKey, state.Workspace, state.Generation, time.Time{})
	if err != nil {
		return DeleteDeploymentResponse{}, err
	}
	if err := orchestrator.EmitCloudEvent(ctx, commandEvent); err != nil {
		return DeleteDeploymentResponse{}, err
	}

	_, err = restate.WithRequestType[orchestrator.DeleteRequest, restate.Void](
		restate.Service[restate.Void](ctx, orchestrator.DeploymentRollbackWorkflowServiceName, "Run"),
	).Request(orchestrator.DeleteRequest{DeploymentKey: deploymentKey})
	if err != nil {
		return DeleteDeploymentResponse{}, err
	}
	updatedState, err := restate.Object[*orchestrator.DeploymentState](ctx, orchestrator.DeploymentStateServiceName, deploymentKey, "GetState").Request(restate.Void{})
	if err != nil {
		return DeleteDeploymentResponse{}, err
	}
	if updatedState == nil {
		return DeleteDeploymentResponse{DeploymentKey: deploymentKey, Status: types.DeploymentDeleted}, nil
	}

	return DeleteDeploymentResponse{DeploymentKey: deploymentKey, Status: updatedState.Status}, nil
}

// Import adopts an existing provider resource through the typed adapter.
func (s *PraxisCommandService) Import(ctx restate.Context, req ImportRequest) (ImportResponse, error) {
	account, _, err := s.resolveWorkspaceDefaults(ctx, req.Account, req.Workspace, nil)
	if err != nil {
		return ImportResponse{}, restate.TerminalError(err, 400)
	}

	adapter, err := s.providers.Get(strings.TrimSpace(req.Kind))
	if err != nil {
		return ImportResponse{}, restate.TerminalError(err, 400)
	}
	if strings.TrimSpace(req.ResourceID) == "" {
		return ImportResponse{}, restate.TerminalError(fmt.Errorf("resource ID is required"), 400)
	}
	if strings.TrimSpace(req.Region) == "" {
		return ImportResponse{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
	}

	key, err := adapter.BuildImportKey(req.Region, req.ResourceID)
	if err != nil {
		return ImportResponse{}, restate.TerminalError(err, 400)
	}

	status, outputs, err := adapter.Import(ctx, key, account, types.ImportRef{
		ResourceID: req.ResourceID,
		Mode:       req.Mode,
		Account:    account,
	})
	if err != nil {
		return ImportResponse{}, err
	}

	commandEvent, err := orchestrator.NewCommandImportEvent(key, req.Workspace, account, req.Region, req.ResourceID, req.Kind, time.Time{})
	if err != nil {
		return ImportResponse{}, err
	}
	if err := orchestrator.EmitCloudEvent(ctx, commandEvent); err != nil {
		return ImportResponse{}, err
	}
	_, err = restate.WithRequestType[eventing.ResourceEventOwner, restate.Void](
		restate.Object[restate.Void](ctx, eventing.ResourceEventOwnerServiceName, key, "Upsert"),
	).Request(eventing.ResourceEventOwner{
		StreamKey:    key,
		Workspace:    req.Workspace,
		Generation:   0,
		ResourceName: req.ResourceID,
		ResourceKind: req.Kind,
	})
	if err != nil {
		return ImportResponse{}, err
	}

	return ImportResponse{Key: key, Status: status, Outputs: outputs}, nil
}
