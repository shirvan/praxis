package s3_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/internal/drivers/s3"
)

func TestHasDrift_NoDrift(t *testing.T) {
	spec := s3.S3BucketSpec{
		BucketName: "test-bucket",
		Region:     "us-east-1",
		Versioning: true,
		Encryption: s3.EncryptionSpec{Enabled: true, Algorithm: "AES256"},
		Tags:       map[string]string{"env": "prod"},
	}
	obs := s3.ObservedState{
		Region:           "us-east-1",
		VersioningStatus: "Enabled",
		EncryptionAlgo:   "AES256",
		Tags:             map[string]string{"env": "prod"},
	}
	assert.False(t, s3.HasDrift(spec, obs))
}

func TestHasDrift_VersioningDrift(t *testing.T) {
	spec := s3.S3BucketSpec{BucketName: "test", Region: "us-east-1", Versioning: true}
	obs := s3.ObservedState{Region: "us-east-1", VersioningStatus: "Suspended"}
	assert.True(t, s3.HasDrift(spec, obs))
}

func TestHasDrift_EncryptionDrift(t *testing.T) {
	spec := s3.S3BucketSpec{BucketName: "test", Region: "us-east-1", Encryption: s3.EncryptionSpec{Enabled: true, Algorithm: "aws:kms"}}
	obs := s3.ObservedState{Region: "us-east-1", EncryptionAlgo: "AES256"}
	assert.True(t, s3.HasDrift(spec, obs))
}

func TestHasDrift_TagDrift(t *testing.T) {
	spec := s3.S3BucketSpec{BucketName: "test", Region: "us-east-1", Tags: map[string]string{"env": "prod"}}
	obs := s3.ObservedState{Region: "us-east-1", Tags: map[string]string{"env": "staging"}}
	assert.True(t, s3.HasDrift(spec, obs))
}

func TestHasDrift_EmptyTagsNoDrift(t *testing.T) {
	spec := s3.S3BucketSpec{BucketName: "test", Region: "us-east-1", Tags: map[string]string{}}
	obs := s3.ObservedState{Region: "us-east-1", Tags: nil}
	assert.False(t, s3.HasDrift(spec, obs))
}

func TestHasDrift_DefaultEncryptionNoDrift(t *testing.T) {
	spec := s3.S3BucketSpec{BucketName: "test", Region: "us-east-1"}
	obs := s3.ObservedState{Region: "us-east-1", EncryptionAlgo: "AES256"}
	assert.False(t, s3.HasDrift(spec, obs))
}

func TestHasDrift_VersioningSuspendedNoDrift(t *testing.T) {
	spec := s3.S3BucketSpec{BucketName: "test", Region: "us-east-1", Versioning: false}
	obs := s3.ObservedState{Region: "us-east-1", VersioningStatus: "Suspended"}
	assert.False(t, s3.HasDrift(spec, obs))
}

func TestHasDrift_VersioningEmptyStringNoDrift(t *testing.T) {
	spec := s3.S3BucketSpec{BucketName: "test", Region: "us-east-1", Versioning: false}
	obs := s3.ObservedState{Region: "us-east-1", VersioningStatus: ""}
	assert.False(t, s3.HasDrift(spec, obs))
}

func TestComputeFieldDiffs_NoDrift(t *testing.T) {
	spec := s3.S3BucketSpec{
		BucketName: "test", Region: "us-east-1",
		Versioning: true,
		Encryption: s3.EncryptionSpec{Enabled: true, Algorithm: "AES256"},
	}
	obs := s3.ObservedState{
		Region: "us-east-1", VersioningStatus: "Enabled", EncryptionAlgo: "AES256",
	}
	diffs := s3.ComputeFieldDiffs(spec, obs)
	assert.Empty(t, diffs)
}

func TestComputeFieldDiffs_AllDrifts(t *testing.T) {
	spec := s3.S3BucketSpec{
		BucketName: "test", Region: "us-east-1",
		Versioning: true,
		Encryption: s3.EncryptionSpec{Enabled: true, Algorithm: "aws:kms"},
		Tags:       map[string]string{"env": "prod"},
	}
	obs := s3.ObservedState{
		Region:           "us-east-1",
		VersioningStatus: "Suspended",
		EncryptionAlgo:   "AES256",
		Tags:             map[string]string{"env": "staging"},
	}
	diffs := s3.ComputeFieldDiffs(spec, obs)
	assert.Len(t, diffs, 3)
	paths := make(map[string]bool)
	for _, d := range diffs {
		paths[d.Path] = true
	}
	assert.True(t, paths["spec.versioning"])
	assert.True(t, paths["spec.encryption.algorithm"])
	assert.True(t, paths["tags.env"])
}
