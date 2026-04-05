package ecrrepo

import (
	"github.com/shirvan/praxis/internal/drivers"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServiceName(t *testing.T) {
	drv := NewECRRepositoryDriver(nil)
	assert.Equal(t, "ECRRepository", drv.ServiceName())
}

func TestApplyDefaults_ImageTagMutability(t *testing.T) {
	spec := applyDefaults(ECRRepositorySpec{})
	assert.Equal(t, "MUTABLE", spec.ImageTagMutability)
}

func TestApplyDefaults_PreservesExistingValue(t *testing.T) {
	spec := applyDefaults(ECRRepositorySpec{ImageTagMutability: "IMMUTABLE"})
	assert.Equal(t, "IMMUTABLE", spec.ImageTagMutability)
}

func TestApplyDefaults_NilTags(t *testing.T) {
	spec := applyDefaults(ECRRepositorySpec{Tags: nil})
	assert.NotNil(t, spec.Tags)
	assert.Empty(t, spec.Tags)
}

func TestApplyDefaults_PreservesExistingTags(t *testing.T) {
	spec := applyDefaults(ECRRepositorySpec{Tags: map[string]string{"env": "prod"}})
	assert.Equal(t, "prod", spec.Tags["env"])
}

func TestValidateProvisionSpec_Valid(t *testing.T) {
	spec := ECRRepositorySpec{Region: "us-east-1", RepositoryName: "my-repo"}
	assert.NoError(t, validateProvisionSpec(spec))
}

func TestValidateProvisionSpec_MissingRegion(t *testing.T) {
	spec := ECRRepositorySpec{RepositoryName: "my-repo"}
	assert.Error(t, validateProvisionSpec(spec))
}

func TestValidateProvisionSpec_MissingRepositoryName(t *testing.T) {
	spec := ECRRepositorySpec{Region: "us-east-1"}
	assert.Error(t, validateProvisionSpec(spec))
}

func TestValidateProvisionSpec_KMSWithoutKey(t *testing.T) {
	spec := ECRRepositorySpec{
		Region:         "us-east-1",
		RepositoryName: "my-repo",
		EncryptionConfiguration: &EncryptionConfiguration{
			EncryptionType: "KMS",
		},
	}
	assert.Error(t, validateProvisionSpec(spec))
}

func TestValidateProvisionSpec_KMSWithKey(t *testing.T) {
	spec := ECRRepositorySpec{
		Region:         "us-east-1",
		RepositoryName: "my-repo",
		EncryptionConfiguration: &EncryptionConfiguration{
			EncryptionType: "KMS",
			KmsKey:         "arn:aws:kms:us-east-1:123456789012:key/abc-123",
		},
	}
	assert.NoError(t, validateProvisionSpec(spec))
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		RepositoryArn:              "arn:aws:ecr:us-east-1:123456789012:repository/my-repo",
		RepositoryName:             "my-repo",
		RepositoryUri:              "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-repo",
		RegistryId:                 "123456789012",
		ImageTagMutability:         "IMMUTABLE",
		ImageScanningConfiguration: &ImageScanningConfiguration{ScanOnPush: true},
		EncryptionConfiguration:    &EncryptionConfiguration{EncryptionType: "AES256"},
		RepositoryPolicy:           `{"Version":"2012-10-17"}`,
		Tags:                       map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~my-repo"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "my-repo", spec.RepositoryName)
	assert.Equal(t, "IMMUTABLE", spec.ImageTagMutability)
	assert.True(t, spec.ImageScanningConfiguration.ScanOnPush)
	assert.Equal(t, "AES256", spec.EncryptionConfiguration.EncryptionType)
	assert.Equal(t, `{"Version":"2012-10-17"}`, spec.RepositoryPolicy)
	assert.Equal(t, "prod", spec.Tags["env"])
	_, hasPraxis := spec.Tags["praxis:managed-key"]
	assert.False(t, hasPraxis)
}

func TestSpecFromObserved_Empty(t *testing.T) {
	obs := ObservedState{RepositoryName: "my-repo"}
	spec := specFromObserved(obs)
	assert.Equal(t, "my-repo", spec.RepositoryName)
	assert.Equal(t, "MUTABLE", spec.ImageTagMutability)
}

func TestOutputsFromObserved(t *testing.T) {
	obs := ObservedState{
		RepositoryArn:  "arn:aws:ecr:us-east-1:123456789012:repository/my-repo",
		RepositoryName: "my-repo",
		RepositoryUri:  "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-repo",
		RegistryId:     "123456789012",
	}
	out := outputsFromObserved(obs)
	assert.Equal(t, obs.RepositoryArn, out.RepositoryArn)
	assert.Equal(t, obs.RepositoryName, out.RepositoryName)
	assert.Equal(t, obs.RepositoryUri, out.RepositoryUri)
	assert.Equal(t, obs.RegistryId, out.RegistryId)
}

func TestRegionFromRepositoryARN(t *testing.T) {
	assert.Equal(t, "us-east-1", regionFromRepositoryARN("arn:aws:ecr:us-east-1:123456789012:repository/my-repo"))
	assert.Equal(t, "eu-west-1", regionFromRepositoryARN("arn:aws:ecr:eu-west-1:123456789012:repository/my-repo"))
	assert.Equal(t, "", regionFromRepositoryARN("invalid"))
	assert.Equal(t, "", regionFromRepositoryARN(""))
}

func TestTagsForApply(t *testing.T) {
	tags := tagsForApply(map[string]string{"env": "prod"}, "us-east-1~my-repo")
	assert.Equal(t, "prod", tags["env"])
	assert.Equal(t, "us-east-1~my-repo", tags["praxis:managed-key"])
}

func TestTagsForApply_EmptyManagedKey(t *testing.T) {
	tags := tagsForApply(map[string]string{"env": "prod"}, "")
	assert.Equal(t, "prod", tags["env"])
	_, has := tags["praxis:managed-key"]
	assert.False(t, has)
}

func TestFilterPraxisTags(t *testing.T) {
	tags := drivers.FilterPraxisTags(map[string]string{
		"env":                "prod",
		"praxis:managed-key": "us-east-1~my-repo",
		"praxis:other":       "value",
		"Name":               "my-repo",
	})
	assert.Equal(t, 2, len(tags))
	assert.Equal(t, "prod", tags["env"])
	assert.Equal(t, "my-repo", tags["Name"])
}

func TestFilterPraxisTags_Nil(t *testing.T) {
	tags := drivers.FilterPraxisTags(nil)
	assert.NotNil(t, tags)
	assert.Empty(t, tags)
}

func TestNormalizeJSON(t *testing.T) {
	assert.Equal(t, normalizeJSON(`{"a":"b"}`), normalizeJSON(`{  "a" :  "b"  }`))
	assert.Equal(t, "", normalizeJSON(""))
	assert.Equal(t, "not-json", normalizeJSON("not-json"))
}
