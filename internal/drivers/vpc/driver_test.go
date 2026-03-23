package vpc

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewVPCDriver(nil)
	assert.Equal(t, "VPC", drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		VpcId:              "vpc-123",
		CidrBlock:          "10.0.0.0/16",
		EnableDnsHostnames: true,
		EnableDnsSupport:   true,
		InstanceTenancy:    "default",
		Tags:               map[string]string{"Name": "my-vpc", "praxis:managed-key": "us-east-1~my-vpc"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.CidrBlock, spec.CidrBlock)
	assert.Equal(t, obs.EnableDnsHostnames, spec.EnableDnsHostnames)
	assert.Equal(t, obs.EnableDnsSupport, spec.EnableDnsSupport)
	assert.Equal(t, obs.InstanceTenancy, spec.InstanceTenancy)
	assert.Equal(t, obs.Tags, spec.Tags)
}

func TestSpecFromObserved_Empty(t *testing.T) {
	obs := ObservedState{
		CidrBlock: "10.0.0.0/16",
	}
	spec := specFromObserved(obs)
	assert.Equal(t, "10.0.0.0/16", spec.CidrBlock)
	assert.False(t, spec.EnableDnsHostnames)
	assert.False(t, spec.EnableDnsSupport)
}

func TestSpecFromObserved_NilTags(t *testing.T) {
	obs := ObservedState{
		CidrBlock: "10.0.0.0/16",
		Tags:      nil,
	}
	spec := specFromObserved(obs)
	assert.Nil(t, spec.Tags)
}

func TestDefaultVPCImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultVPCImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultVPCImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultVPCImportMode(types.ModeObserved))
}
