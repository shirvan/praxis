package ecrrepo_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/internal/drivers/ecrrepo"
)

func TestHasDrift_NoDrift(t *testing.T) {
	spec := ecrrepo.ECRRepositorySpec{
		ImageTagMutability:         "MUTABLE",
		ImageScanningConfiguration: &ecrrepo.ImageScanningConfiguration{ScanOnPush: false},
		Tags:                       map[string]string{"env": "prod"},
	}
	obs := ecrrepo.ObservedState{
		ImageTagMutability:         "MUTABLE",
		ImageScanningConfiguration: &ecrrepo.ImageScanningConfiguration{ScanOnPush: false},
		Tags:                       map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~my-repo"},
	}
	assert.False(t, ecrrepo.HasDrift(spec, obs))
}

func TestHasDrift_ImageTagMutabilityChanged(t *testing.T) {
	spec := ecrrepo.ECRRepositorySpec{ImageTagMutability: "IMMUTABLE", Tags: map[string]string{}}
	obs := ecrrepo.ObservedState{ImageTagMutability: "MUTABLE", Tags: map[string]string{}}
	assert.True(t, ecrrepo.HasDrift(spec, obs))
}

func TestHasDrift_ScanOnPushChanged(t *testing.T) {
	spec := ecrrepo.ECRRepositorySpec{
		ImageTagMutability:         "MUTABLE",
		ImageScanningConfiguration: &ecrrepo.ImageScanningConfiguration{ScanOnPush: true},
		Tags:                       map[string]string{},
	}
	obs := ecrrepo.ObservedState{
		ImageTagMutability:         "MUTABLE",
		ImageScanningConfiguration: &ecrrepo.ImageScanningConfiguration{ScanOnPush: false},
		Tags:                       map[string]string{},
	}
	assert.True(t, ecrrepo.HasDrift(spec, obs))
}

func TestHasDrift_TagsChanged(t *testing.T) {
	spec := ecrrepo.ECRRepositorySpec{
		ImageTagMutability: "MUTABLE",
		Tags:               map[string]string{"env": "prod"},
	}
	obs := ecrrepo.ObservedState{
		ImageTagMutability: "MUTABLE",
		Tags:               map[string]string{"env": "dev"},
	}
	assert.True(t, ecrrepo.HasDrift(spec, obs))
}

func TestHasDrift_RepositoryPolicyChanged(t *testing.T) {
	spec := ecrrepo.ECRRepositorySpec{
		ImageTagMutability: "MUTABLE",
		RepositoryPolicy:   `{"Version":"2012-10-17","Statement":[]}`,
		Tags:               map[string]string{},
	}
	obs := ecrrepo.ObservedState{
		ImageTagMutability: "MUTABLE",
		RepositoryPolicy:   `{"Version":"2012-10-17"}`,
		Tags:               map[string]string{},
	}
	assert.True(t, ecrrepo.HasDrift(spec, obs))
}

func TestHasDrift_IgnoresPraxisTags(t *testing.T) {
	spec := ecrrepo.ECRRepositorySpec{
		ImageTagMutability: "MUTABLE",
		Tags:               map[string]string{"env": "prod"},
	}
	obs := ecrrepo.ObservedState{
		ImageTagMutability: "MUTABLE",
		Tags:               map[string]string{"env": "prod", "praxis:managed-key": "x"},
	}
	assert.False(t, ecrrepo.HasDrift(spec, obs))
}

func TestHasDrift_NilAndEmptyTags(t *testing.T) {
	spec := ecrrepo.ECRRepositorySpec{ImageTagMutability: "MUTABLE", Tags: nil}
	obs := ecrrepo.ObservedState{ImageTagMutability: "MUTABLE", Tags: map[string]string{}}
	assert.False(t, ecrrepo.HasDrift(spec, obs))

	spec2 := ecrrepo.ECRRepositorySpec{ImageTagMutability: "MUTABLE", Tags: map[string]string{}}
	obs2 := ecrrepo.ObservedState{ImageTagMutability: "MUTABLE", Tags: nil}
	assert.False(t, ecrrepo.HasDrift(spec2, obs2))
}

func TestHasDrift_ScanningNilVsDefault(t *testing.T) {
	// nil scanning config should match ScanOnPush=false (default)
	spec := ecrrepo.ECRRepositorySpec{ImageTagMutability: "MUTABLE", Tags: map[string]string{}}
	obs := ecrrepo.ObservedState{
		ImageTagMutability:         "MUTABLE",
		ImageScanningConfiguration: &ecrrepo.ImageScanningConfiguration{ScanOnPush: false},
		Tags:                       map[string]string{},
	}
	assert.False(t, ecrrepo.HasDrift(spec, obs))
}

func TestHasDrift_EncryptionImmutableIgnored(t *testing.T) {
	// Encryption difference is reported as immutable-ignored, but still counts as drift for reporting
	spec := ecrrepo.ECRRepositorySpec{
		ImageTagMutability:      "MUTABLE",
		EncryptionConfiguration: &ecrrepo.EncryptionConfiguration{EncryptionType: "KMS", KmsKey: "arn:..."},
		Tags:                    map[string]string{},
	}
	obs := ecrrepo.ObservedState{
		ImageTagMutability:      "MUTABLE",
		EncryptionConfiguration: &ecrrepo.EncryptionConfiguration{EncryptionType: "AES256"},
		Tags:                    map[string]string{},
	}
	assert.True(t, ecrrepo.HasDrift(spec, obs))
}

func TestComputeFieldDiffs_NoChanges(t *testing.T) {
	spec := ecrrepo.ECRRepositorySpec{
		RepositoryName:     "my-repo",
		ImageTagMutability: "MUTABLE",
		Tags:               map[string]string{"env": "prod"},
	}
	obs := ecrrepo.ObservedState{
		RepositoryName:     "my-repo",
		ImageTagMutability: "MUTABLE",
		Tags:               map[string]string{"env": "prod"},
	}
	assert.Empty(t, ecrrepo.ComputeFieldDiffs(spec, obs))
}

func TestComputeFieldDiffs_MutableChanges(t *testing.T) {
	spec := ecrrepo.ECRRepositorySpec{
		RepositoryName:     "my-repo",
		ImageTagMutability: "IMMUTABLE",
		Tags:               map[string]string{"env": "prod"},
	}
	obs := ecrrepo.ObservedState{
		RepositoryName:     "my-repo",
		ImageTagMutability: "MUTABLE",
		Tags:               map[string]string{"env": "dev"},
	}
	diffs := ecrrepo.ComputeFieldDiffs(spec, obs)
	paths := map[string]bool{}
	for _, d := range diffs {
		paths[d.Path] = true
	}
	assert.True(t, paths["spec.imageTagMutability"])
	assert.True(t, paths["spec.tags"])
}

func TestComputeFieldDiffs_ImmutableRepositoryName(t *testing.T) {
	spec := ecrrepo.ECRRepositorySpec{RepositoryName: "new-name", ImageTagMutability: "MUTABLE", Tags: map[string]string{}}
	obs := ecrrepo.ObservedState{RepositoryName: "old-name", ImageTagMutability: "MUTABLE", Tags: map[string]string{}}
	diffs := ecrrepo.ComputeFieldDiffs(spec, obs)
	paths := map[string]bool{}
	for _, d := range diffs {
		paths[d.Path] = true
	}
	assert.True(t, paths["spec.repositoryName (immutable, ignored)"])
}

func TestComputeFieldDiffs_ImmutableEncryption(t *testing.T) {
	spec := ecrrepo.ECRRepositorySpec{
		ImageTagMutability:      "MUTABLE",
		EncryptionConfiguration: &ecrrepo.EncryptionConfiguration{EncryptionType: "KMS", KmsKey: "arn:..."},
		Tags:                    map[string]string{},
	}
	obs := ecrrepo.ObservedState{
		ImageTagMutability:      "MUTABLE",
		EncryptionConfiguration: &ecrrepo.EncryptionConfiguration{EncryptionType: "AES256"},
		Tags:                    map[string]string{},
	}
	diffs := ecrrepo.ComputeFieldDiffs(spec, obs)
	paths := map[string]bool{}
	for _, d := range diffs {
		paths[d.Path] = true
	}
	assert.True(t, paths["spec.encryptionConfiguration (immutable, ignored)"])
}

func TestComputeFieldDiffs_RepositoryPolicyJSONNormalized(t *testing.T) {
	spec := ecrrepo.ECRRepositorySpec{
		ImageTagMutability: "MUTABLE",
		RepositoryPolicy:   `{ "Version" :  "2012-10-17" }`,
		Tags:               map[string]string{},
	}
	obs := ecrrepo.ObservedState{
		ImageTagMutability: "MUTABLE",
		RepositoryPolicy:   `{"Version":"2012-10-17"}`,
		Tags:               map[string]string{},
	}
	// Semantically identical JSON — no drift
	assert.Empty(t, ecrrepo.ComputeFieldDiffs(spec, obs))
}

func TestComputeFieldDiffs_IgnoresPraxisTags(t *testing.T) {
	spec := ecrrepo.ECRRepositorySpec{
		ImageTagMutability: "MUTABLE",
		Tags:               map[string]string{"env": "dev"},
	}
	obs := ecrrepo.ObservedState{
		ImageTagMutability: "MUTABLE",
		Tags:               map[string]string{"env": "dev", "praxis:managed-key": "k"},
	}
	assert.Empty(t, ecrrepo.ComputeFieldDiffs(spec, obs))
}
