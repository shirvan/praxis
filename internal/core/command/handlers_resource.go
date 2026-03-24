package command

import (
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/orchestrator"
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

	workflowID := deploymentKey + "-delete"
	restate.WorkflowSend(ctx, orchestrator.DeploymentDeleteWorkflowServiceName, workflowID, "Run").Send(
		orchestrator.DeleteRequest{DeploymentKey: deploymentKey},
		restate.WithIdempotencyKey(workflowID),
	)

	return DeleteDeploymentResponse{DeploymentKey: deploymentKey, Status: types.DeploymentDeleting}, nil
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

	return ImportResponse{Key: key, Status: status, Outputs: outputs}, nil
}
