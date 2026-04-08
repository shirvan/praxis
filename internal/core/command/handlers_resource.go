// handlers_resource.go implements the Delete, Rollback, and Import handlers.
//
// These handlers manage the non-apply lifecycle of deployments and resources:
//
//   - DeleteDeployment: Tears down all resources in a deployment by dispatching
//     a delete workflow that processes resources in reverse dependency order.
//   - RollbackDeployment: Synchronously rolls back a failed/cancelled deployment
//     by deleting only the resources that were successfully created during the
//     failed run, restoring the previous state.
//   - Import: Adopts an existing cloud resource into Praxis management without
//     recreating it.
//
// State guards prevent invalid transitions (e.g., deleting an already-deleted
// deployment, rolling back a successful one).
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
//
// State guards:
//   - If already deleting → return current status (idempotent).
//   - If already deleted → TerminalError 409 (conflict).
//   - Otherwise → emit audit event, dispatch async delete workflow.
//
// The delete workflow is dispatched asynchronously via WorkflowSend so this
// handler returns immediately. The workflow processes resources in reverse
// topological order to respect dependency constraints.
func (s *PraxisCommandService) DeleteDeployment(ctx restate.Context, req DeleteDeploymentRequest) (DeleteDeploymentResponse, error) {
	deploymentKey := strings.TrimSpace(req.DeploymentKey)
	if deploymentKey == "" {
		return DeleteDeploymentResponse{}, restate.TerminalError(fmt.Errorf("deployment key is required"), 400)
	}

	// Fetch current deployment state from the durable DeploymentStateObj.
	// This is a Restate request-response call — journaled and replayed.
	state, err := restate.Object[*orchestrator.DeploymentState](ctx, orchestrator.DeploymentStateServiceName, deploymentKey, "GetState").Request(restate.Void{})
	if err != nil {
		return DeleteDeploymentResponse{}, err
	}
	if state == nil {
		return DeleteDeploymentResponse{}, restate.TerminalError(fmt.Errorf("deployment %q not found", deploymentKey), 404)
	}
	// State guard: idempotent if already deleting.
	if state.Status == types.DeploymentDeleting {
		return DeleteDeploymentResponse{DeploymentKey: deploymentKey, Status: types.DeploymentDeleting}, nil
	}
	// State guard: conflict if already deleted — unless --force is set,
	// which allows re-running deletion to clean up resources that were
	// skipped during a previous delete (e.g. due to dependency failures).
	if state.Status == types.DeploymentDeleted {
		if !req.Force {
			return DeleteDeploymentResponse{}, restate.TerminalError(fmt.Errorf("deployment %q is already deleted", deploymentKey), 409)
		}
	}

	// If the deployment is currently provisioning (Running or Pending),
	// cancel the in-progress apply workflow first. This sets a durable flag
	// that the apply loop polls — it will stop dispatching new resources and
	// let in-flight ones finish. The delete workflow then picks up whatever
	// state the deployment is in and tears everything down.
	//
	// This makes delete a "flush" operation: users can always unstick a
	// deployment by deleting it, regardless of what's in progress.
	if state.Status == types.DeploymentRunning || state.Status == types.DeploymentPending {
		if _, err := restate.Object[restate.Void](ctx, orchestrator.DeploymentStateServiceName, deploymentKey, "RequestCancel").Request(restate.Void{}); err != nil {
			return DeleteDeploymentResponse{}, fmt.Errorf("failed to cancel in-progress operation: %w", err)
		}
	}

	// Emit a structured audit event before dispatching the workflow.
	commandEvent, err := orchestrator.NewCommandDeleteEvent(deploymentKey, state.Workspace, state.Generation, time.Time{})
	if err != nil {
		return DeleteDeploymentResponse{}, err
	}
	if err := orchestrator.EmitCloudEvent(ctx, commandEvent); err != nil {
		return DeleteDeploymentResponse{}, err
	}

	// Dispatch the async delete workflow. The workflow ID includes the
	// state's UpdatedAt timestamp so that retries after a failed delete
	// (e.g. switching from non-force to --force) get a fresh workflow
	// execution. Concurrent calls to DeleteDeployment for the same
	// deployment see the same UpdatedAt and are still deduplicated.
	workflowID := fmt.Sprintf("%s-delete-%d", deploymentKey, state.UpdatedAt.UnixNano())
	restate.WorkflowSend(ctx, orchestrator.DeploymentDeleteWorkflowServiceName, workflowID, "Run").Send(
		orchestrator.DeleteRequest{DeploymentKey: deploymentKey, Force: req.Force, Orphan: req.Orphan, Parallelism: req.Parallelism},
		restate.WithIdempotencyKey(workflowID),
	)

	return DeleteDeploymentResponse{DeploymentKey: deploymentKey, Status: types.DeploymentDeleting}, nil
}

// RollbackDeployment synchronously rolls back a failed or cancelled deployment.
// Unlike DeleteDeployment (which tears down everything asynchronously),
// Rollback runs synchronously and only removes the resources that were
// created during the failed deployment generation. This restores the
// deployment to its pre-failure state.
//
// State guards:
//   - Only allowed from Failed or Cancelled status.
//   - If already deleting → return current status.
//   - If already deleted → TerminalError 409.
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
	// Rollback is only valid from terminal failure states. Successful
	// deployments should use Delete instead.
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

	// Unlike Delete, Rollback calls the workflow synchronously (Request
	// instead of Send) because the caller needs the final state to know
	// whether the rollback succeeded. The workflow ID includes UpdatedAt
	// so that retries after a failed rollback get a fresh execution, while
	// concurrent calls for the same deployment are still deduplicated.
	workflowID := fmt.Sprintf("%s-rollback-%d", deploymentKey, state.UpdatedAt.UnixNano())
	_, err = restate.WithRequestType[orchestrator.DeleteRequest, restate.Void](
		restate.Workflow[restate.Void](ctx, orchestrator.DeploymentRollbackWorkflowServiceName, workflowID, "Run"),
	).Request(orchestrator.DeleteRequest{DeploymentKey: deploymentKey, Force: req.Force, Orphan: req.Orphan, Parallelism: req.Parallelism})
	if err != nil {
		return DeleteDeploymentResponse{}, err
	}
	// Re-fetch state after the synchronous rollback to return the final status.
	updatedState, err := restate.Object[*orchestrator.DeploymentState](ctx, orchestrator.DeploymentStateServiceName, deploymentKey, "GetState").Request(restate.Void{})
	if err != nil {
		return DeleteDeploymentResponse{}, err
	}
	if updatedState == nil {
		return DeleteDeploymentResponse{DeploymentKey: deploymentKey, Status: types.DeploymentDeleted}, nil
	}

	return DeleteDeploymentResponse{DeploymentKey: deploymentKey, Status: updatedState.Status}, nil
}

// Import adopts an existing provider resource into Praxis management through
// the typed adapter. The resource is not created — it already exists in the
// cloud provider. Import reads the resource's current state and records it in
// Praxis so that subsequent Apply/Plan operations can manage it.
//
// Flow:
//  1. Resolve the account.
//  2. Look up the provider adapter for the resource kind.
//  3. Build the canonical resource key from region + resource ID.
//  4. Call the adapter's Import method to read current state.
//  5. Emit an audit event and register a resource event owner.
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

	// BuildImportKey constructs the canonical resource key (e.g.,
	// "AWS::S3::Bucket/us-east-1/my-bucket") from the region and resource ID.
	key, err := adapter.BuildImportKey(req.Region, req.ResourceID)
	if err != nil {
		return ImportResponse{}, restate.TerminalError(err, 400)
	}

	// adapter.Import reads the resource's live state from the cloud provider.
	// The adapter may use restate.Run internally to journal API calls.
	status, outputs, err := adapter.Import(ctx, key, account, types.ImportRef{
		ResourceID: req.ResourceID,
		Mode:       req.Mode,
		Account:    account,
	})
	if err != nil {
		return ImportResponse{}, err
	}

	// Emit a structured audit event recording the import operation.
	commandEvent, err := orchestrator.NewCommandImportEvent(key, req.Workspace, account, req.Region, req.ResourceID, req.Kind, time.Time{})
	if err != nil {
		return ImportResponse{}, err
	}
	if err := orchestrator.EmitCloudEvent(ctx, commandEvent); err != nil {
		return ImportResponse{}, err
	}

	// Register a resource event owner so that the eventing subsystem knows
	// this resource key belongs to a workspace and can route future events.
	_, err = restate.WithRequestType[eventing.ResourceEventOwner, restate.Void](
		restate.Object[restate.Void](ctx, eventing.ResourceEventOwnerServiceName, key, "Upsert"),
	).Request(eventing.ResourceEventOwner{
		StreamKey:    key,
		Workspace:    req.Workspace,
		Generation:   0, // Generation 0 indicates an imported (not deployed) resource.
		ResourceName: req.ResourceID,
		ResourceKind: req.Kind,
	})
	if err != nil {
		return ImportResponse{}, err
	}

	return ImportResponse{Key: key, Status: status, Outputs: outputs}, nil
}
