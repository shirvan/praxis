//go:build integration

// Point-in-time rollback tests: every generation's plan is snapshotted, and
// `RollbackTo` replays a stored known-good plan — converging changed specs,
// deleting resources added since, and re-provisioning resources removed
// since. These tests verify all three inverse changes against Moto, the
// known-good gating, and the history retention bound.
package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/drivers/loggroup"
	"github.com/shirvan/praxis/pkg/types"
)

func taggedBucketTemplate(bucketName, version string) string {
	return fmt.Sprintf(`
resources: {
	bucketA: {
		apiVersion: "praxis.io/alpha"
		kind:       "S3Bucket"
		metadata: { name: %q }
		spec: {
			region: "us-east-1"
			tags: { release: %q }
		}
	}
}
`, bucketName, version)
}

func taggedBucketPlusSecondTemplate(bucketA, version, bucketB string) string {
	return fmt.Sprintf(`
resources: {
	bucketA: {
		apiVersion: "praxis.io/alpha"
		kind:       "S3Bucket"
		metadata: { name: %q }
		spec: {
			region: "us-east-1"
			tags: { release: %q }
		}
	}
	bucketB: {
		apiVersion: "praxis.io/alpha"
		kind:       "S3Bucket"
		metadata: { name: %q }
		spec: { region: "us-east-1" }
	}
}
`, bucketA, version, bucketB)
}

// failingLogGroupDriver impersonates the LogGroup driver service with a
// Provision that always fails terminally. The core test stack does not bind
// the real loggroup driver, so binding this fake gives rollback tests a
// deterministic, Moto-independent way to fail one generation (Moto's own
// validation proved too lenient and state-dependent to rely on).
type failingLogGroupDriver struct{}

func (failingLogGroupDriver) ServiceName() string { return "LogGroup" }

func (failingLogGroupDriver) Provision(ctx restate.ObjectContext, spec loggroup.LogGroupSpec) (loggroup.LogGroupOutputs, error) {
	return loggroup.LogGroupOutputs{}, restate.TerminalError(errors.New("injected provision failure"), 400)
}

// Rollback's removed-resource cleanup dispatches Delete for the failed
// resource; succeed silently so the replay can proceed.
func (failingLogGroupDriver) Delete(ctx restate.ObjectContext) error {
	return nil
}

// The workflow's observe-before-act path reads the driver's shared handlers
// before dispatching Provision; the fake must answer them.
func (failingLogGroupDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	return types.StatusResponse{Status: types.StatusPending}, nil
}

func (failingLogGroupDriver) GetInputs(ctx restate.ObjectSharedContext) (loggroup.LogGroupSpec, error) {
	return loggroup.LogGroupSpec{}, nil
}

func (failingLogGroupDriver) GetOutputs(ctx restate.ObjectSharedContext) (loggroup.LogGroupOutputs, error) {
	return loggroup.LogGroupOutputs{}, nil
}

func rollbackTo(t *testing.T, env *coreTestEnv, deployKey string, generation int64) (types.DeployResponse, error) {
	t.Helper()
	return ingress.Service[types.RollbackToRequest, types.DeployResponse](
		env.ingress, "PraxisCommandService", "RollbackTo",
	).Request(t.Context(), types.RollbackToRequest{
		DeploymentKey: deployKey,
		ToGeneration:  generation,
	})
}

func bucketReleaseTag(t *testing.T, client *s3sdk.Client, bucket string) string {
	t.Helper()
	tags, err := client.GetBucketTagging(context.Background(), &s3sdk.GetBucketTaggingInput{
		Bucket: aws.String(bucket),
	})
	require.NoError(t, err)
	for _, tag := range tags.TagSet {
		if aws.ToString(tag.Key) == "release" {
			return aws.ToString(tag.Value)
		}
	}
	return ""
}

// TestRollbackTo_RevertsChangesAndDeletesAdded is the headline flow:
// gen1 = bucket A tagged v1; gen2 = tag changed to v2 plus bucket B added.
// Rolling back to gen1 must revert the tag AND delete bucket B.
func TestRollbackTo_RevertsChangesAndDeletesAdded(t *testing.T) {
	env := setupCoreStack(t)
	bucketA := uniqueName(t, "rba")
	bucketB := uniqueName(t, "rbb")
	deployKey := "test-rollback-" + bucketA

	applyAndWaitComplete(t, env, deployKey, taggedBucketTemplate(bucketA, "v1"), false)
	applyAndWaitComplete(t, env, deployKey, taggedBucketPlusSecondTemplate(bucketA, "v2", bucketB), false)
	require.Equal(t, "v2", bucketReleaseTag(t, env.s3Client, bucketA))
	_, err := env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{Bucket: aws.String(bucketB)})
	require.NoError(t, err, "gen2 must have created bucket B")

	resp, err := rollbackTo(t, env, deployKey, 1)
	require.NoError(t, err)
	require.Equal(t, deployKey, resp.DeploymentKey)

	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete}, 60*time.Second)
	require.Equal(t, types.DeploymentComplete, state.Status, "rollback error: %v", state.Error)
	assert.Equal(t, int64(3), state.Generation, "the rollback is itself a new generation")
	assert.Equal(t, "rollback://gen-1", state.TemplatePath, "provenance must show the rollback target")

	assert.Equal(t, "v1", bucketReleaseTag(t, env.s3Client, bucketA), "changed spec must be reverted")
	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{Bucket: aws.String(bucketB)})
	require.Error(t, err, "resource added after the target generation must be deleted")

	// History records all three generations with terminal statuses.
	records, err := ingress.Object[restate.Void, []orchestrator.GenerationRecord](
		env.ingress, "DeploymentStateObj", deployKey, "ListGenerations",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	require.Len(t, records, 3)
	for _, record := range records {
		assert.Equal(t, types.DeploymentComplete, record.FinalStatus, "generation %d", record.Generation)
	}
	assert.Equal(t, "rollback://gen-1", records[2].TemplatePath)
}

// TestRollbackTo_RestoresRemovedResource: gen2 drops bucket B; rolling back
// to gen1 re-provisions it.
func TestRollbackTo_RestoresRemovedResource(t *testing.T) {
	env := setupCoreStack(t)
	bucketA := uniqueName(t, "rsa")
	bucketB := uniqueName(t, "rsb")
	deployKey := "test-rollback-restore-" + bucketA

	applyAndWaitComplete(t, env, deployKey, taggedBucketPlusSecondTemplate(bucketA, "v1", bucketB), false)
	applyAndWaitComplete(t, env, deployKey, taggedBucketTemplate(bucketA, "v1"), false)
	_, err := env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{Bucket: aws.String(bucketB)})
	require.Error(t, err, "gen2 must have deleted bucket B")

	_, err = rollbackTo(t, env, deployKey, 1)
	require.NoError(t, err)
	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete}, 60*time.Second)
	require.Equal(t, types.DeploymentComplete, state.Status, "rollback error: %v", state.Error)

	_, err = env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{Bucket: aws.String(bucketB)})
	require.NoError(t, err, "resource removed after the target generation must be re-provisioned")
}

// TestRollbackTo_RecoversFromFailedDeployment: a failed generation cannot be
// a target, but rolling back PAST it to the last known-good generation
// restores the deployment.
func TestRollbackTo_RecoversFromFailedDeployment(t *testing.T) {
	env := setupCoreStack(t, restate.Reflect(failingLogGroupDriver{}))
	bucketA := uniqueName(t, "rcv")
	deployKey := "test-rollback-recover-" + bucketA

	applyAndWaitComplete(t, env, deployKey, taggedBucketTemplate(bucketA, "v1"), false)

	// Generation 2 fails at provision time: the bound fake LogGroup driver
	// rejects every provision terminally, so the deployment fails.
	badTemplate := fmt.Sprintf(`
resources: {
	bucketA: {
		apiVersion: "praxis.io/alpha"
		kind:       "S3Bucket"
		metadata: { name: %q }
		spec: {
			region: "us-east-1"
			tags: { release: "v2" }
		}
	}
	badLogs: {
		apiVersion: "praxis.io/alpha"
		kind:       "LogGroup"
		metadata: { name: "/praxis/rollback/injected-failure" }
		spec: { region: "us-east-1" }
	}
}
`, bucketA)
	_, err := ingress.Service[commandApplyRequestAlias, commandApplyResponseAlias](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), commandApplyRequestAlias{
		Template:      badTemplate,
		DeploymentKey: deployKey,
		Variables:     accountVariables(),
	})
	require.NoError(t, err)
	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentFailed, types.DeploymentComplete}, 60*time.Second)
	require.Equal(t, types.DeploymentFailed, state.Status, "generation 2 should have failed")

	// The failed generation is not a known-good target.
	_, err = rollbackTo(t, env, deployKey, 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a known-good target")

	// Rolling back to generation 1 restores the last good state.
	_, err = rollbackTo(t, env, deployKey, 1)
	require.NoError(t, err)
	state = pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete}, 60*time.Second)
	require.Equal(t, types.DeploymentComplete, state.Status, "rollback error: %v", state.Error)
	assert.Equal(t, "v1", bucketReleaseTag(t, env.s3Client, bucketA))
}

// TestRollbackTo_UnknownGenerationFails: targets outside the recorded history
// are rejected with guidance.
func TestRollbackTo_UnknownGenerationFails(t *testing.T) {
	env := setupCoreStack(t)
	bucketA := uniqueName(t, "unk")
	deployKey := "test-rollback-unknown-" + bucketA

	applyAndWaitComplete(t, env, deployKey, taggedBucketTemplate(bucketA, "v1"), false)

	_, err := rollbackTo(t, env, deployKey, 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no recorded generation 42")
}

// TestRollbackTo_HistoryRetentionBound: only the most recent generations keep
// snapshots; older ones are pruned and can no longer be targets. Driven via
// InitDeployment/Finalize directly so 12 generations don't need 12 workflows.
func TestRollbackTo_HistoryRetentionBound(t *testing.T) {
	env := setupCoreStack(t)
	deployKey := "test-rollback-retention-" + uniqueName(t, "dep")

	for range 12 {
		now := time.Now().UTC()
		_, err := ingress.Object[orchestrator.DeploymentPlan, int64](
			env.ingress, "DeploymentStateObj", deployKey, "InitDeployment",
		).Request(t.Context(), orchestrator.DeploymentPlan{
			Key:       deployKey,
			CreatedAt: now,
		})
		require.NoError(t, err)
		_, err = ingress.Object[orchestrator.FinalizeRequest, restate.Void](
			env.ingress, "DeploymentStateObj", deployKey, "Finalize",
		).Request(t.Context(), orchestrator.FinalizeRequest{
			Status:    types.DeploymentComplete,
			UpdatedAt: now,
		})
		require.NoError(t, err)
	}

	records, err := ingress.Object[restate.Void, []orchestrator.GenerationRecord](
		env.ingress, "DeploymentStateObj", deployKey, "ListGenerations",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	require.Len(t, records, 10, "history must be bounded to the retention limit")
	assert.Equal(t, int64(3), records[0].Generation, "oldest retained generation")
	assert.Equal(t, int64(12), records[9].Generation)

	// Pruned generations have no snapshot.
	_, err = ingress.Object[int64, *orchestrator.DeploymentPlan](
		env.ingress, "DeploymentStateObj", deployKey, "GetPlanSnapshot",
	).Request(t.Context(), int64(1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no stored plan")

	// Retained generations still have theirs.
	plan, err := ingress.Object[int64, *orchestrator.DeploymentPlan](
		env.ingress, "DeploymentStateObj", deployKey, "GetPlanSnapshot",
	).Request(t.Context(), int64(12))
	require.NoError(t, err)
	require.NotNil(t, plan)
}

// Local aliases keep this file readable without importing internal/core/command.
type commandApplyRequestAlias = types.ApplyRequest
type commandApplyResponseAlias = types.ApplyResponse
