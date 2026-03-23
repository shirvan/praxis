package eip

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewElasticIPDriver(nil)
	assert.Equal(t, "ElasticIP", drv.ServiceName())
}

func TestSpecFromObserved_RoundTrip(t *testing.T) {
	obs := ObservedState{
		AllocationId:       "eipalloc-123",
		PublicIp:           "203.0.113.10",
		Domain:             "vpc",
		NetworkBorderGroup: "us-east-1",
		Tags:               map[string]string{"Name": "web-eip", "env": "dev", "praxis:managed-key": "us-east-1~web-eip"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.Domain, spec.Domain)
	assert.Equal(t, obs.NetworkBorderGroup, spec.NetworkBorderGroup)
	assert.Equal(t, map[string]string{"Name": "web-eip", "env": "dev"}, spec.Tags)
	assert.Empty(t, spec.PublicIpv4Pool)
}

func TestOutputsFromObserved(t *testing.T) {
	outputs := outputsFromObserved(ObservedState{
		AllocationId:       "eipalloc-123",
		PublicIp:           "203.0.113.10",
		Domain:             "vpc",
		NetworkBorderGroup: "us-east-1",
	}, "us-east-1", "123456789012")

	assert.Equal(t, "eipalloc-123", outputs.AllocationId)
	assert.Equal(t, "203.0.113.10", outputs.PublicIp)
	assert.Equal(t, "vpc", outputs.Domain)
	assert.Equal(t, "us-east-1", outputs.NetworkBorderGroup)
	assert.Equal(t, "arn:aws:ec2:us-east-1:123456789012:elastic-ip/eipalloc-123", outputs.ARN)
}

func TestDefaultEIPImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultEIPImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultEIPImportMode(types.ModeManaged))
}
