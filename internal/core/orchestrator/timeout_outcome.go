package orchestrator

import (
	"fmt"
	"time"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

// timeoutOutcomeMessage makes the timeout contract explicit: the workflow
// stops waiting, but the durable driver invocation is not cancelled and may
// still complete against the provider.
func timeoutOutcomeMessage(operation string, timeout time.Duration) string {
	return fmt.Sprintf("%s timed out after %s; provider outcome is unknown and the durable driver invocation continues", operation, timeout)
}

// recordTimeoutEvidence overlays Unknown conditions on the existing Error
// state and emits the dedicated timeout event. The ordinary failure recorder
// still owns dependency skipping and the resource error event.
func recordTimeoutEvidence(
	ctx restate.WorkflowContext,
	deploymentKey string,
	workspace string,
	generation int64,
	exec *executionState,
	resourceName string,
	resourceKind string,
	operation string,
	timeout time.Duration,
	errMsg string,
) error {
	now, err := currentTime(ctx)
	if err != nil {
		return err
	}
	conditions := timeoutConditions(exec.conditionsFor(resourceName), now, errMsg)
	exec.setConditions(resourceName, conditions)
	if err := updateDeploymentResource(ctx, deploymentKey, ResourceUpdate{
		Name:       resourceName,
		Status:     types.DeploymentResourceError,
		Error:      errMsg,
		Conditions: conditions,
	}); err != nil {
		return err
	}
	timeoutEvent, err := NewResourceTimeoutEvent(deploymentKey, workspace, generation, time.Time{}, resourceName, resourceKind, operation, timeout)
	if err != nil {
		return err
	}
	EmitDeploymentCloudEventBestEffort(ctx, timeoutEvent)
	return nil
}
