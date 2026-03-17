package sg

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- specFromObserved tests ---

func TestSpecFromObserved_FullyPopulated(t *testing.T) {
	obs := ObservedState{
		GroupId:     "sg-123",
		GroupName:   "my-sg",
		Description: "Test SG",
		VpcId:       "vpc-abc",
		IngressRules: []NormalizedRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
		},
		EgressRules: []NormalizedRule{
			{Direction: "egress", Protocol: "all", FromPort: 0, ToPort: 0, Target: "cidr:0.0.0.0/0"},
		},
		Tags: map[string]string{"env": "prod"},
	}

	spec := specFromObserved(obs)

	assert.Equal(t, "my-sg", spec.GroupName)
	assert.Equal(t, "Test SG", spec.Description)
	assert.Equal(t, "vpc-abc", spec.VpcId)
	assert.Len(t, spec.IngressRules, 1)
	assert.Equal(t, "tcp", spec.IngressRules[0].Protocol)
	assert.Equal(t, int32(80), spec.IngressRules[0].FromPort)
	assert.Equal(t, "0.0.0.0/0", spec.IngressRules[0].CidrBlock)
	assert.Len(t, spec.EgressRules, 1)
	assert.Equal(t, "-1", spec.EgressRules[0].Protocol) // "all" denormalized to "-1"
	assert.Equal(t, map[string]string{"env": "prod"}, spec.Tags)
}

func TestSpecFromObserved_Empty(t *testing.T) {
	obs := ObservedState{
		GroupName:   "empty-sg",
		Description: "Empty",
		VpcId:       "vpc-123",
	}

	spec := specFromObserved(obs)

	assert.Equal(t, "empty-sg", spec.GroupName)
	assert.Empty(t, spec.IngressRules)
	assert.Empty(t, spec.EgressRules)
}

func TestSpecFromObserved_NilTags(t *testing.T) {
	obs := ObservedState{
		GroupName:   "no-tags",
		Description: "No tags",
		VpcId:       "vpc-123",
		Tags:        nil,
	}

	spec := specFromObserved(obs)
	assert.Nil(t, spec.Tags)
}

// --- ServiceName tests ---

func TestServiceName(t *testing.T) {
	drv := NewSecurityGroupDriver(nil)
	assert.Equal(t, "SecurityGroup", drv.ServiceName())
}

// --- extractCidr tests ---

func TestExtractCidr(t *testing.T) {
	assert.Equal(t, "10.0.0.0/8", extractCidr("cidr:10.0.0.0/8"))
	assert.Equal(t, "0.0.0.0/0", extractCidr("cidr:0.0.0.0/0"))
	assert.Equal(t, "raw", extractCidr("raw"))
}
