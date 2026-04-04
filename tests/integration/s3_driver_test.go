//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/drivers/s3"
	"github.com/shirvan/praxis/pkg/types"
)

// uniqueBucket generates a unique bucket name for each test to prevent collisions.
func uniqueBucket(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	// Ensure S3 naming rules: max 63 chars, lowercase, no underscores
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func TestS3Provision_CreatesRealBucket(t *testing.T) {
	client, s3Client := setupS3Driver(t)
	bucket := uniqueBucket(t)
	key := bucket

	// Provision the bucket
	outputs, err := ingress.Object[s3.S3BucketSpec, s3.S3BucketOutputs](
		client, "S3Bucket", key, "Provision",
	).Request(t.Context(), s3.S3BucketSpec{
		Account:    integrationAccountName,
		BucketName: bucket,
		Region:     "us-east-1",
		Versioning: true,
		Encryption: s3.EncryptionSpec{Enabled: true, Algorithm: "AES256"},
		Tags:       map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, bucket, outputs.BucketName)
	assert.Contains(t, outputs.ARN, bucket)

	// Verify bucket exists in Moto
	_, err = s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
		Bucket: &bucket,
	})
	require.NoError(t, err, "bucket should exist in Moto")
}

func TestS3Provision_Idempotent(t *testing.T) {
	client, _ := setupS3Driver(t)
	bucket := uniqueBucket(t)
	key := bucket
	spec := s3.S3BucketSpec{
		Account:    integrationAccountName,
		BucketName: bucket,
		Region:     "us-east-1",
		Versioning: true,
		Encryption: s3.EncryptionSpec{Enabled: true, Algorithm: "AES256"},
	}

	// First provision
	_, err := ingress.Object[s3.S3BucketSpec, s3.S3BucketOutputs](
		client, "S3Bucket", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	// Second provision with same spec — should succeed without error
	outputs2, err := ingress.Object[s3.S3BucketSpec, s3.S3BucketOutputs](
		client, "S3Bucket", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, bucket, outputs2.BucketName)
}

func TestS3Import_ExistingBucket(t *testing.T) {
	client, s3Client := setupS3Driver(t)
	bucket := uniqueBucket(t)

	// Create bucket directly in Moto
	_, err := s3Client.CreateBucket(context.Background(), &s3sdk.CreateBucketInput{
		Bucket: &bucket,
	})
	require.NoError(t, err)

	// Import via driver
	key := bucket
	outputs, err := ingress.Object[types.ImportRef, s3.S3BucketOutputs](
		client, "S3Bucket", key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: bucket,
		Mode:       types.ModeManaged,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, bucket, outputs.BucketName)
	assert.Contains(t, outputs.ARN, bucket)
}

func TestS3Delete_RemovesBucket(t *testing.T) {
	client, s3Client := setupS3Driver(t)
	bucket := uniqueBucket(t)
	key := bucket

	// Provision
	_, err := ingress.Object[s3.S3BucketSpec, s3.S3BucketOutputs](
		client, "S3Bucket", key, "Provision",
	).Request(t.Context(), s3.S3BucketSpec{
		Account:    integrationAccountName,
		BucketName: bucket,
		Region:     "us-east-1",
	})
	require.NoError(t, err)

	// Delete
	_, err = ingress.Object[restate.Void, restate.Void](
		client, "S3Bucket", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	// Verify bucket is gone
	_, err = s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{
		Bucket: &bucket,
	})
	require.Error(t, err, "bucket should be deleted from Moto")
}

func TestS3Delete_NonEmptyBucketFails(t *testing.T) {
	client, s3Client := setupS3Driver(t)
	bucket := uniqueBucket(t)
	key := bucket

	// Provision
	_, err := ingress.Object[s3.S3BucketSpec, s3.S3BucketOutputs](
		client, "S3Bucket", key, "Provision",
	).Request(t.Context(), s3.S3BucketSpec{
		Account:    integrationAccountName,
		BucketName: bucket,
		Region:     "us-east-1",
	})
	require.NoError(t, err)

	// Upload an object directly to make bucket non-empty
	objectKey := "test-object.txt"
	body := strings.NewReader("test content")
	_, err = s3Client.PutObject(context.Background(), &s3sdk.PutObjectInput{
		Bucket: &bucket,
		Key:    &objectKey,
		Body:   body,
	})
	require.NoError(t, err)

	// Delete should fail with terminal error (409)
	_, err = ingress.Object[restate.Void, restate.Void](
		client, "S3Bucket", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.Error(t, err, "delete should fail on non-empty bucket")
}

func TestS3Reconcile_DetectsAndFixesDrift(t *testing.T) {
	client, s3Client := setupS3Driver(t)
	bucket := uniqueBucket(t)
	key := bucket

	// Provision with versioning enabled
	_, err := ingress.Object[s3.S3BucketSpec, s3.S3BucketOutputs](
		client, "S3Bucket", key, "Provision",
	).Request(t.Context(), s3.S3BucketSpec{
		Account:    integrationAccountName,
		BucketName: bucket,
		Region:     "us-east-1",
		Versioning: true,
		Encryption: s3.EncryptionSpec{Enabled: true, Algorithm: "AES256"},
	})
	require.NoError(t, err)

	// Introduce drift: suspend versioning directly via S3 API
	_, err = s3Client.PutBucketVersioning(context.Background(), &s3sdk.PutBucketVersioningInput{
		Bucket: &bucket,
		VersioningConfiguration: &s3types.VersioningConfiguration{
			Status: s3types.BucketVersioningStatusSuspended,
		},
	})
	require.NoError(t, err)

	// Trigger reconcile
	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, "S3Bucket", key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")

	// Verify versioning was re-enabled
	versioning, err := s3Client.GetBucketVersioning(context.Background(), &s3sdk.GetBucketVersioningInput{
		Bucket: &bucket,
	})
	require.NoError(t, err)
	assert.Equal(t, s3types.BucketVersioningStatusEnabled, versioning.Status)
}

func TestS3GetStatus_ReturnsReady(t *testing.T) {
	client, _ := setupS3Driver(t)
	bucket := uniqueBucket(t)
	key := bucket

	// Provision
	_, err := ingress.Object[s3.S3BucketSpec, s3.S3BucketOutputs](
		client, "S3Bucket", key, "Provision",
	).Request(t.Context(), s3.S3BucketSpec{
		Account:    integrationAccountName,
		BucketName: bucket,
		Region:     "us-east-1",
	})
	require.NoError(t, err)

	// GetStatus
	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "S3Bucket", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
