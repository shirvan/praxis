//go:build integration

// Approval-gate tests: deployments into a protected workspace must suspend in
// AwaitingApproval before dispatching anything, resume on approve, terminate
// on reject, leave an audit trail in the event stream, and survive a Restate
// crash while suspended.
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/core/command"
	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/core/workspace"
	"github.com/shirvan/praxis/pkg/types"
)

func configureProtectedWorkspace(t *testing.T, env *coreTestEnv) string {
	t.Helper()
	name := "protected-" + uniqueName(t, "ws")
	_, err := ingress.Object[workspace.WorkspaceConfig, restate.Void](
		env.ingress, workspace.WorkspaceServiceName, name, "Configure",
	).Request(t.Context(), workspace.WorkspaceConfig{
		Name:      name,
		Account:   integrationAccountName,
		Region:    "us-east-1",
		Protected: true,
	})
	require.NoError(t, err)
	return name
}

func applyToWorkspace(t *testing.T, env *coreTestEnv, deployKey, workspaceName, template string) {
	t.Helper()
	_, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      template,
		DeploymentKey: deployKey,
		Workspace:     workspaceName,
		Variables:     accountVariables(),
	})
	require.NoError(t, err)
}

func decideDeployment(t *testing.T, env *coreTestEnv, handler, deployKey, comment string) (types.ApprovalResponse, error) {
	t.Helper()
	return ingress.Service[types.ApprovalRequest, types.ApprovalResponse](
		env.ingress, "PraxisCommandService", handler,
	).Request(t.Context(), types.ApprovalRequest{
		DeploymentKey: deployKey,
		DecidedBy:     "integration-operator",
		Comment:       comment,
	})
}

// TestApprovalGate_ApproveResumesDeployment is the happy path: protected
// deployment suspends with nothing provisioned, approval resumes it to
// Complete, and the audit events appear in order.
func TestApprovalGate_ApproveResumesDeployment(t *testing.T) {
	env := setupCoreStack(t)
	workspaceName := configureProtectedWorkspace(t, env)
	bucketName := uniqueName(t, "appr")
	deployKey := "test-approve-" + bucketName

	applyToWorkspace(t, env, deployKey, workspaceName, simpleS3Template(bucketName))

	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentAwaitingApproval}, 30*time.Second)
	require.Equal(t, types.DeploymentAwaitingApproval, state.Status)

	// Nothing may be provisioned while suspended.
	_, err := env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{Bucket: aws.String(bucketName)})
	require.Error(t, err, "no resource may exist before approval")

	// Re-apply onto the suspended deployment must be rejected.
	_, err = ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      simpleS3Template(bucketName),
		DeploymentKey: deployKey,
		Workspace:     workspaceName,
		Variables:     accountVariables(),
	})
	require.Error(t, err, "apply onto an awaiting deployment must be guarded")
	assert.Contains(t, err.Error(), "awaiting approval")

	resp, err := decideDeployment(t, env, "Approve", deployKey, "looks good")
	require.NoError(t, err)
	assert.Equal(t, types.DeploymentRunning, resp.Status)

	state = pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete}, 60*time.Second)
	require.Equal(t, types.DeploymentComplete, state.Status, "error: %v", state.Error)
	assert.Nil(t, state.Approval, "the approval gate must be cleared after resuming")

	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{Bucket: aws.String(bucketName)})
	require.NoError(t, err, "the bucket must exist after approval")

	events := pollDeploymentEventTypes(t, env.ingress, deployKey, []string{
		orchestrator.EventTypeDeploymentApprovalRequested,
		orchestrator.EventTypeDeploymentApprovalApproved,
		orchestrator.EventTypeDeploymentStarted,
		orchestrator.EventTypeDeploymentCompleted,
	}, 30*time.Second)
	requestedSeq, approvedSeq := int64(-1), int64(-1)
	for _, record := range events {
		switch record.Event.Type() {
		case orchestrator.EventTypeDeploymentApprovalRequested:
			requestedSeq = record.Sequence
		case orchestrator.EventTypeDeploymentApprovalApproved:
			approvedSeq = record.Sequence
		}
	}
	require.GreaterOrEqual(t, requestedSeq, int64(0))
	assert.Greater(t, approvedSeq, requestedSeq, "approved must follow requested in the stream")
}

// TestApprovalGate_RejectCancelsDeployment: rejection terminates the
// deployment as Cancelled, provisions nothing, and records who/why.
func TestApprovalGate_RejectCancelsDeployment(t *testing.T) {
	env := setupCoreStack(t)
	workspaceName := configureProtectedWorkspace(t, env)
	bucketName := uniqueName(t, "rej")
	deployKey := "test-reject-" + bucketName

	applyToWorkspace(t, env, deployKey, workspaceName, simpleS3Template(bucketName))
	pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentAwaitingApproval}, 30*time.Second)

	resp, err := decideDeployment(t, env, "Reject", deployKey, "wrong change window")
	require.NoError(t, err)
	assert.Equal(t, types.DeploymentCancelled, resp.Status)

	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentCancelled}, 60*time.Second)
	assert.Contains(t, state.Error, "rejected by integration-operator")
	assert.Contains(t, state.Error, "wrong change window")

	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{Bucket: aws.String(bucketName)})
	require.Error(t, err, "a rejected deployment must not provision anything")

	events := pollDeploymentEventTypes(t, env.ingress, deployKey, []string{
		orchestrator.EventTypeDeploymentApprovalRequested,
		orchestrator.EventTypeDeploymentApprovalRejected,
		orchestrator.EventTypeDeploymentCancelled,
	}, 30*time.Second)
	for _, record := range events {
		if record.Event.Type() == orchestrator.EventTypeDeploymentApprovalRejected {
			payload := string(record.Event.Data())
			assert.Contains(t, payload, "integration-operator", "audit event must name the decider")
			assert.Contains(t, payload, "wrong change window", "audit event must carry the comment")
		}
	}
}

// TestApprovalGate_DecisionOnNonWaitingDeploymentFails: approving something
// that is not suspended is a conflict, not a silent no-op.
func TestApprovalGate_DecisionOnNonWaitingDeploymentFails(t *testing.T) {
	env := setupCoreStack(t)
	bucketName := uniqueName(t, "nw")
	deployKey := "test-nonwaiting-" + bucketName

	applyAndWaitComplete(t, env, deployKey, simpleS3Template(bucketName), false)

	_, err := decideDeployment(t, env, "Approve", deployKey, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not awaiting approval")

	_, err = decideDeployment(t, env, "Approve", "no-such-deployment", "")
	require.Error(t, err)
}

// TestApprovalGate_SurvivesRestateRestart: the marquee durability property —
// a deployment suspended at its gate survives a Restate crash and can still
// be approved afterwards.
func TestApprovalGate_SurvivesRestateRestart(t *testing.T) {
	env := setupCoreStack(t)
	workspaceName := configureProtectedWorkspace(t, env)
	bucketName := uniqueName(t, "crashappr")
	deployKey := "test-crash-approve-" + bucketName

	applyToWorkspace(t, env, deployKey, workspaceName, simpleS3Template(bucketName))
	pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentAwaitingApproval}, 30*time.Second)

	env.env.RestartRestate(t)
	env.ingress = env.env.Ingress()

	resp, err := decideDeployment(t, env, "Approve", deployKey, "approved after restart")
	require.NoError(t, err)
	assert.Equal(t, types.DeploymentRunning, resp.Status)

	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete}, 120*time.Second)
	require.Equal(t, types.DeploymentComplete, state.Status, "error: %v", state.Error)

	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{Bucket: aws.String(bucketName)})
	require.NoError(t, err, "the bucket must exist after the post-restart approval")
}
