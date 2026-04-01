package ecrpolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServiceName(t *testing.T) {
	drv := NewECRLifecyclePolicyDriver(nil)
	assert.Equal(t, "ECRLifecyclePolicy", drv.ServiceName())
}

func TestValidateProvisionSpec_Valid(t *testing.T) {
	spec := ECRLifecyclePolicySpec{
		Region:              "us-east-1",
		RepositoryName:      "my-repo",
		LifecyclePolicyText: `{"rules":[]}`,
	}
	assert.NoError(t, validateProvisionSpec(spec))
}

func TestValidateProvisionSpec_MissingRegion(t *testing.T) {
	spec := ECRLifecyclePolicySpec{
		RepositoryName:      "my-repo",
		LifecyclePolicyText: `{"rules":[]}`,
	}
	assert.Error(t, validateProvisionSpec(spec))
}

func TestValidateProvisionSpec_MissingRepositoryName(t *testing.T) {
	spec := ECRLifecyclePolicySpec{
		Region:              "us-east-1",
		LifecyclePolicyText: `{"rules":[]}`,
	}
	assert.Error(t, validateProvisionSpec(spec))
}

func TestValidateProvisionSpec_EmptyPolicyText(t *testing.T) {
	spec := ECRLifecyclePolicySpec{
		Region:         "us-east-1",
		RepositoryName: "my-repo",
	}
	assert.Error(t, validateProvisionSpec(spec))
}

func TestValidateProvisionSpec_InvalidJSON(t *testing.T) {
	spec := ECRLifecyclePolicySpec{
		Region:              "us-east-1",
		RepositoryName:      "my-repo",
		LifecyclePolicyText: "not-json",
	}
	assert.Error(t, validateProvisionSpec(spec))
}

func TestValidatePolicyJSON_Valid(t *testing.T) {
	assert.NoError(t, validatePolicyJSON(`{"rules":[]}`))
}

func TestValidatePolicyJSON_Empty(t *testing.T) {
	assert.Error(t, validatePolicyJSON(""))
	assert.Error(t, validatePolicyJSON("   "))
}

func TestValidatePolicyJSON_Invalid(t *testing.T) {
	assert.Error(t, validatePolicyJSON("not-json"))
}

func TestOutputsFromObserved(t *testing.T) {
	obs := ObservedState{
		RepositoryName: "my-repo",
		RepositoryArn:  "arn:aws:ecr:us-east-1:123456789012:repository/my-repo",
		RegistryId:     "123456789012",
	}
	out := outputsFromObserved(obs)
	assert.Equal(t, "my-repo", out.RepositoryName)
	assert.Equal(t, obs.RepositoryArn, out.RepositoryArn)
	assert.Equal(t, obs.RegistryId, out.RegistryId)
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		RepositoryName:      "my-repo",
		RepositoryArn:       "arn:aws:ecr:us-east-1:123456789012:repository/my-repo",
		LifecyclePolicyText: `{"rules":[]}`,
	}
	spec := specFromObserved(obs)
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "my-repo", spec.RepositoryName)
	assert.Equal(t, `{"rules":[]}`, spec.LifecyclePolicyText)
}

func TestRegionFromRepositoryARN(t *testing.T) {
	assert.Equal(t, "us-east-1", regionFromRepositoryARN("arn:aws:ecr:us-east-1:123456789012:repository/my-repo"))
	assert.Equal(t, "eu-west-1", regionFromRepositoryARN("arn:aws:ecr:eu-west-1:123456789012:repository/my-repo"))
	assert.Equal(t, "", regionFromRepositoryARN("invalid"))
	assert.Equal(t, "", regionFromRepositoryARN(""))
}

func TestNormalizePolicy(t *testing.T) {
	assert.Equal(t, normalizePolicy(`{"a":"b"}`), normalizePolicy(`{  "a" :  "b"  }`))
	assert.Equal(t, "", normalizePolicy(""))
	assert.Equal(t, "not-json", normalizePolicy("not-json"))
}
