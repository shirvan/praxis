package s3

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- specFromObserved tests ---
// specFromObserved is the bridge between Import's observed AWS state and the
// desired spec baseline. Getting this wrong means import-then-reconcile sees
// phantom drift on the very first cycle.

func TestSpecFromObserved_FullyPopulated(t *testing.T) {
	obs := ObservedState{
		Region:           "eu-west-1",
		VersioningStatus: "Enabled",
		EncryptionAlgo:   "aws:kms",
		Tags:             map[string]string{"env": "prod", "team": "platform"},
	}

	spec := specFromObserved("my-bucket", obs)

	assert.Equal(t, "my-bucket", spec.BucketName)
	assert.Equal(t, "eu-west-1", spec.Region)
	assert.True(t, spec.Versioning)
	assert.True(t, spec.Encryption.Enabled)
	assert.Equal(t, "aws:kms", spec.Encryption.Algorithm)
	assert.Equal(t, map[string]string{"env": "prod", "team": "platform"}, spec.Tags)
}

func TestSpecFromObserved_VersioningSuspended(t *testing.T) {
	obs := ObservedState{
		Region:           "us-east-1",
		VersioningStatus: "Suspended",
		EncryptionAlgo:   "AES256",
	}

	spec := specFromObserved("test-bucket", obs)

	assert.False(t, spec.Versioning)
	assert.Equal(t, "AES256", spec.Encryption.Algorithm)
}

func TestSpecFromObserved_VersioningNeverEnabled(t *testing.T) {
	obs := ObservedState{
		Region:           "us-east-1",
		VersioningStatus: "",
		EncryptionAlgo:   "AES256",
	}

	spec := specFromObserved("test-bucket", obs)

	assert.False(t, spec.Versioning)
}

func TestSpecFromObserved_NoEncryption(t *testing.T) {
	// When no encryption is reported (empty algo), specFromObserved defaults
	// to AES256 since that's the AWS default since January 2023.
	obs := ObservedState{
		Region:           "us-east-1",
		VersioningStatus: "Enabled",
		EncryptionAlgo:   "",
	}

	spec := specFromObserved("test-bucket", obs)

	assert.True(t, spec.Encryption.Enabled)
	assert.Equal(t, "AES256", spec.Encryption.Algorithm)
}

func TestSpecFromObserved_NilTags(t *testing.T) {
	obs := ObservedState{
		Region:           "us-east-1",
		VersioningStatus: "Enabled",
		EncryptionAlgo:   "AES256",
		Tags:             nil,
	}

	spec := specFromObserved("test-bucket", obs)
	assert.Nil(t, spec.Tags)
}

// --- ServiceName tests ---

func TestServiceName(t *testing.T) {
	drv := NewS3BucketDriver(nil)
	assert.Equal(t, "S3Bucket", drv.ServiceName())
}
