//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

type resourceJSON struct {
	Kind       string               `json:"kind"`
	Key        string               `json:"key"`
	Status     types.ResourceStatus `json:"status"`
	Mode       types.Mode           `json:"mode"`
	Reconcile  types.ReconcileMode  `json:"reconcile"`
	Generation int64                `json:"generation"`
	Conditions []types.Condition    `json:"conditions"`
	Inputs     map[string]any       `json:"inputs"`
	Outputs    map[string]any       `json:"outputs"`
}

type generationJSON struct {
	Generation  int64                  `json:"generation"`
	FinalStatus types.DeploymentStatus `json:"finalStatus"`
}

func (env *topology) requireDriftUpdateRollback(t *testing.T) {
	suffix := acceptanceSuffix()
	bucketName := "praxis-drift-" + suffix
	deploymentKey := "drift-" + suffix
	observePath := writeAcceptanceTemplate(t, driftBucketTemplate(bucketName, "v1", "observe"))
	autoPath := writeAcceptanceTemplate(t, driftBucketTemplate(bucketName, "v2", "auto"))

	cleanupNeeded := true
	t.Cleanup(func() {
		if !cleanupNeeded {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		_, _ = env.runCLIContext(ctx, "delete", "Deployment/"+deploymentKey, "--yes", "--wait", "--timeout", "2m")
	})

	first := env.deployAndWait(t, deploymentKey, observePath)
	require.Equal(t, types.DeploymentComplete, first.Status, first.Error)
	resource := deploymentResource(t, first, "bucket")
	require.Equal(t, "v1", bucketTags(t, env.s3, bucketName)["release"])

	// Observe mode must detect drift without writing it away. Ready remains the
	// coarse health signal; DriftFree=False carries the important extra state.
	putBucketTag(t, env.s3, bucketName, "release", "external-observe")
	var observed types.ReconcileResult
	env.runCLIJSON(t, &observed, "reconcile", resource.Kind+"/"+resource.Key)
	assert.True(t, observed.Drift)
	assert.False(t, observed.Correcting)
	assert.Equal(t, "external-observe", bucketTags(t, env.s3, bucketName)["release"], "observe mode must not mutate provider state")
	assertDriftStatus(t, env.getResource(t, resource.Kind, resource.Key), types.ModeManaged, types.ReconcileModeObserve, types.ConditionFalse, types.ReasonDriftDetected)

	// A normal apply is still authoritative even when periodic reconciliation
	// had been observe-only. This is generation 2 and also switches the policy.
	second := env.deployAndWait(t, deploymentKey, autoPath)
	require.Equal(t, types.DeploymentComplete, second.Status, second.Error)
	resource = deploymentResource(t, second, "bucket")
	assert.Equal(t, "v2", bucketTags(t, env.s3, bucketName)["release"])

	// Auto mode reports that correction happened and restores provider state.
	putBucketTag(t, env.s3, bucketName, "release", "external-auto")
	var corrected types.ReconcileResult
	env.runCLIJSON(t, &corrected, "reconcile", resource.Kind+"/"+resource.Key)
	assert.True(t, corrected.Drift)
	assert.True(t, corrected.Correcting)
	assert.Equal(t, "v2", bucketTags(t, env.s3, bucketName)["release"], "auto mode must restore declared state")
	assertDriftStatus(t, env.getResource(t, resource.Kind, resource.Key), types.ModeManaged, types.ReconcileModeAuto, types.ConditionTrue, types.ReasonDriftCorrected)

	var generations []generationJSON
	env.runCLIJSON(t, &generations, "list", "generations", deploymentKey)
	require.Len(t, generations, 2)
	for index, generation := range generations {
		assert.Equal(t, int64(index+1), generation.Generation)
		assert.Equal(t, types.DeploymentComplete, generation.FinalStatus)
	}

	// Rollback is an ordinary new generation that replays both the old desired
	// provider state and its reconciliation policy.
	var rolledBack types.DeploymentDetail
	env.runCLIJSON(t, &rolledBack,
		"rollback", deploymentKey, "--to", "1", "--wait", "--poll-interval", "100ms",
	)
	require.Equal(t, types.DeploymentComplete, rolledBack.Status, rolledBack.Error)
	resource = deploymentResource(t, rolledBack, "bucket")
	assert.Equal(t, "v1", bucketTags(t, env.s3, bucketName)["release"], "rollback must restore generation 1 provider state")
	rolledBackStatus := env.getResource(t, resource.Kind, resource.Key)
	assert.Equal(t, types.ReconcileModeObserve, rolledBackStatus.Reconcile, "rollback must restore generation 1 lifecycle policy")
	assert.Equal(t, int64(3), rolledBackStatus.Generation)

	var deleted types.DeploymentDetail
	env.runCLIJSON(t, &deleted,
		"delete", "Deployment/"+deploymentKey, "--yes", "--wait",
		"--timeout", "3m",
	)
	require.Equal(t, types.DeploymentDeleted, deleted.Status, deleted.Error)
	assertBucketMissing(t, env.s3, bucketName)
	cleanupNeeded = false
}

func (env *topology) requireObservedImport(t *testing.T) {
	bucketName := "praxis-import-" + acceptanceSuffix()
	_, err := env.s3.CreateBucket(t.Context(), &s3sdk.CreateBucketInput{Bucket: aws.String(bucketName)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = env.s3.DeleteBucket(context.Background(), &s3sdk.DeleteBucketInput{Bucket: aws.String(bucketName)})
	})
	putBucketTag(t, env.s3, bucketName, "owner", "external")

	var imported types.ImportResponse
	env.runCLIJSON(t, &imported,
		"import", "S3Bucket", "--id", bucketName, "--region", "us-east-1", "--account", "local", "--observe",
	)
	require.Equal(t, bucketName, imported.Key)
	require.Equal(t, types.StatusReady, imported.Status)
	assert.Equal(t, bucketName, imported.Outputs["bucketName"])

	status := env.getResource(t, "S3Bucket", imported.Key)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeObserved, status.Mode)
	assert.Equal(t, types.ReconcileModeObserve, status.Reconcile)
	assert.Equal(t, "external", bucketTags(t, env.s3, bucketName)["owner"])

	putBucketTag(t, env.s3, bucketName, "owner", "changed-outside-praxis")
	var reconciliation types.ReconcileResult
	env.runCLIJSON(t, &reconciliation, "reconcile", "S3Bucket/"+imported.Key)
	assert.True(t, reconciliation.Drift)
	assert.False(t, reconciliation.Correcting)
	assert.Equal(t, "changed-outside-praxis", bucketTags(t, env.s3, bucketName)["owner"])
	assertDriftStatus(t, env.getResource(t, "S3Bucket", imported.Key), types.ModeObserved, types.ReconcileModeObserve, types.ConditionFalse, types.ReasonDriftDetected)

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	output, err := env.runCLIContext(ctx, "delete", "S3Bucket/"+imported.Key, "--yes")
	require.Error(t, err, "observed resources must reject provider deletion")
	assert.Contains(t, output, "Observed mode")
	_, err = env.s3.HeadBucket(t.Context(), &s3sdk.HeadBucketInput{Bucket: aws.String(bucketName)})
	require.NoError(t, err, "rejected delete must leave the external resource intact")
}

func (env *topology) deployAndWait(t *testing.T, deploymentKey, templatePath string) types.DeploymentDetail {
	t.Helper()
	var detail types.DeploymentDetail
	env.runCLIJSON(t, &detail,
		"deploy", templatePath, "--account", "local", "--key", deploymentKey,
		"--yes", "--wait", "--poll-interval", "100ms", "--timeout", "3m",
	)
	return detail
}

func (env *topology) getResource(t *testing.T, kind, key string) resourceJSON {
	t.Helper()
	var status resourceJSON
	env.runCLIJSON(t, &status, "get", kind+"/"+key)
	return status
}

func assertDriftStatus(t *testing.T, status resourceJSON, ownership types.Mode, reconcile types.ReconcileMode, conditionStatus, reason string) {
	t.Helper()
	assert.Equal(t, types.StatusReady, status.Status, "drift under an operator-selected policy remains healthy")
	assert.Equal(t, ownership, status.Mode)
	assert.Equal(t, reconcile, status.Reconcile)
	condition, ok := types.GetCondition(status.Conditions, types.ConditionDriftFree)
	require.True(t, ok, "CLI resource view must expose the DriftFree condition")
	assert.Equal(t, conditionStatus, condition.Status)
	assert.Equal(t, reason, condition.Reason)
}

func writeAcceptanceTemplate(t *testing.T, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "resource.cue")
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	return path
}

func driftBucketTemplate(bucketName, release, reconcile string) string {
	return fmt.Sprintf(`resources: bucket: {
	apiVersion: "praxis.io/alpha"
	kind:       "S3Bucket"
	metadata: name: %q
	lifecycle: reconcile: %q
	spec: {
		region:     "us-east-1"
		versioning: false
		acl:        "private"
		encryption: {enabled: true, algorithm: "AES256"}
		tags: release: %q
	}
}
`, bucketName, reconcile, release)
}

func putBucketTag(t *testing.T, client *s3sdk.Client, bucket, key, value string) {
	t.Helper()
	tags := bucketTags(t, client, bucket)
	tags[key] = value
	tagSet := make([]s3types.Tag, 0, len(tags))
	for tagKey, tagValue := range tags {
		tagSet = append(tagSet, s3types.Tag{Key: aws.String(tagKey), Value: aws.String(tagValue)})
	}
	_, err := client.PutBucketTagging(t.Context(), &s3sdk.PutBucketTaggingInput{
		Bucket:  aws.String(bucket),
		Tagging: &s3types.Tagging{TagSet: tagSet},
	})
	require.NoError(t, err)
}
