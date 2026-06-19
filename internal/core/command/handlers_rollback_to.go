// handlers_rollback_to.go implements point-in-time rollback: reverting a
// deployment to the state captured by a previous known-good generation.
//
// Rollback is deliberately implemented as a replay, not a bespoke inverse
// diff: every generation's full DeploymentPlan is snapshotted by
// InitDeployment, and rolling back resubmits that stored plan through the
// same submit path as apply. The existing machinery then produces exactly
// the inverse changes — drivers converge specs that changed, resources added
// since the target generation are deleted (removeMissing), and resources
// removed since are re-provisioned. The rollback itself becomes a new,
// snapshotted generation, so rollbacks are themselves roll-back-able, pass
// the submit guard, and honor workspace protection (approval gates).
package command

import (
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/pkg/types"
)

// RollbackTo reverts a deployment to a previous known-good generation.
func (s *PraxisCommandService) RollbackTo(ctx restate.Context, req types.RollbackToRequest) (DeployResponse, error) {
	key := strings.TrimSpace(req.DeploymentKey)
	if key == "" {
		return DeployResponse{}, restate.TerminalError(fmt.Errorf("deployment key is required"), 400)
	}
	if req.ToGeneration <= 0 {
		return DeployResponse{}, restate.TerminalError(fmt.Errorf("target generation must be a positive integer"), 400)
	}

	// Only generations that finished Complete are known-good targets.
	history, err := restate.Object[[]orchestrator.GenerationRecord](
		ctx, orchestrator.DeploymentStateServiceName, key, "ListGenerations",
	).Request(restate.Void{})
	if err != nil {
		return DeployResponse{}, err
	}
	var target *orchestrator.GenerationRecord
	for i := range history {
		if history[i].Generation == req.ToGeneration {
			target = &history[i]
			break
		}
	}
	if target == nil {
		return DeployResponse{}, restate.TerminalError(fmt.Errorf(
			"deployment %q has no recorded generation %d (history is bounded; run 'praxis list generations %s')",
			key, req.ToGeneration, key), 404)
	}
	if target.FinalStatus != types.DeploymentComplete {
		status := string(target.FinalStatus)
		if status == "" {
			status = "still in flight"
		}
		return DeployResponse{}, restate.TerminalError(fmt.Errorf(
			"generation %d of deployment %q is not a known-good target (final status: %s); only Complete generations can be rolled back to",
			req.ToGeneration, key, status), 409)
	}

	snapshot, err := restate.Object[*orchestrator.DeploymentPlan](
		ctx, orchestrator.DeploymentStateServiceName, key, "GetPlanSnapshot",
	).Request(req.ToGeneration)
	if err != nil {
		return DeployResponse{}, err
	}

	// Replay the stored plan through the normal submit path. removeMissing
	// deletes resources added after the target generation; fresh flags mean
	// no force-replace carryover and default retry behavior.
	resultKey, status, err := s.submitPlanResources(
		ctx,
		key,
		snapshot.Account,
		snapshot.Workspace,
		snapshot.Variables,
		snapshot.Resources,
		fmt.Sprintf("rollback://gen-%d", req.ToGeneration),
		true,  // removeMissing
		false, // orphanRemoved
		nil,   // forceReplace
		false, // allowReplace
		snapshot.MaxParallelism,
		nil, // maxRetries: default
	)
	if err != nil {
		return DeployResponse{}, err
	}
	return DeployResponse{DeploymentKey: resultKey, Status: status}, nil
}
