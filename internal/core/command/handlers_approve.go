// handlers_approve.go implements the approval-gate decision handlers.
//
// A deployment into a protected workspace suspends in AwaitingApproval on a
// durable awakeable created by the deployment workflow. Approve and Reject
// validate that the deployment is actually waiting, then resolve that
// awakeable with the operator's decision. The workflow — as the single writer
// of deployment status — performs the resulting transition and emits the
// audit events.
package command

import (
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/pkg/types"
)

// Approve resumes a deployment suspended at its approval gate.
func (s *PraxisCommandService) Approve(ctx restate.Context, req types.ApprovalRequest) (types.ApprovalResponse, error) {
	return s.decideApproval(ctx, req, true)
}

// Reject terminates a deployment suspended at its approval gate. The
// deployment finalizes as Cancelled without dispatching any resource.
func (s *PraxisCommandService) Reject(ctx restate.Context, req types.ApprovalRequest) (types.ApprovalResponse, error) {
	return s.decideApproval(ctx, req, false)
}

func (s *PraxisCommandService) decideApproval(ctx restate.Context, req types.ApprovalRequest, approved bool) (types.ApprovalResponse, error) {
	key := strings.TrimSpace(req.DeploymentKey)
	if key == "" {
		return types.ApprovalResponse{}, restate.TerminalError(fmt.Errorf("deployment key is required"), 400)
	}

	state, err := restate.Object[*orchestrator.DeploymentState](
		ctx, orchestrator.DeploymentStateServiceName, key, "GetState",
	).Request(restate.Void{})
	if err != nil {
		return types.ApprovalResponse{}, err
	}
	if state == nil {
		return types.ApprovalResponse{}, restate.TerminalError(fmt.Errorf("deployment %q not found", key), 404)
	}
	if state.Status != types.DeploymentAwaitingApproval || state.Approval == nil || state.Approval.AwakeableID == "" {
		return types.ApprovalResponse{}, restate.TerminalError(
			fmt.Errorf("deployment %q is %s, not awaiting approval", key, state.Status), 409)
	}

	restate.ResolveAwakeable(ctx, state.Approval.AwakeableID, types.ApprovalDecision{
		Approved:  approved,
		DecidedBy: strings.TrimSpace(req.DecidedBy),
		Comment:   strings.TrimSpace(req.Comment),
	})

	// The workflow performs the actual transition; report the status the
	// decision leads to.
	resulting := types.DeploymentRunning
	if !approved {
		resulting = types.DeploymentCancelled
	}
	return types.ApprovalResponse{DeploymentKey: key, Status: resulting}, nil
}
