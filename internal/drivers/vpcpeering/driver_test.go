package vpcpeering

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewVPCPeeringDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		RequesterVpcId:   "vpc-req",
		AccepterVpcId:    "vpc-acc",
		Status:           "active",
		RequesterOptions: &PeeringOptions{AllowDnsResolutionFromRemoteVpc: true},
		AccepterOptions:  &PeeringOptions{AllowDnsResolutionFromRemoteVpc: false},
		Tags:             map[string]string{"Name": "peer", "praxis:managed-key": "us-east-1~peer"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.RequesterVpcId, spec.RequesterVpcId)
	assert.Equal(t, obs.AccepterVpcId, spec.AccepterVpcId)
	assert.True(t, spec.AutoAccept)
	assert.Equal(t, true, spec.RequesterOptions.AllowDnsResolutionFromRemoteVpc)
	assert.Equal(t, false, spec.AccepterOptions.AllowDnsResolutionFromRemoteVpc)
	assert.Equal(t, map[string]string{"Name": "peer"}, spec.Tags)
}

func TestOutputsFromObserved(t *testing.T) {
	obs := ObservedState{
		VpcPeeringConnectionId: "pcx-123",
		RequesterVpcId:         "vpc-req",
		AccepterVpcId:          "vpc-acc",
		RequesterCidrBlock:     "10.0.0.0/16",
		AccepterCidrBlock:      "10.1.0.0/16",
		Status:                 "active",
		RequesterOwnerId:       "111111111111",
		AccepterOwnerId:        "111111111111",
	}

	out := outputsFromObserved(obs)
	assert.Equal(t, obs.VpcPeeringConnectionId, out.VpcPeeringConnectionId)
	assert.Equal(t, obs.RequesterVpcId, out.RequesterVpcId)
	assert.Equal(t, obs.AccepterVpcId, out.AccepterVpcId)
	assert.Equal(t, obs.RequesterCidrBlock, out.RequesterCidrBlock)
	assert.Equal(t, obs.AccepterCidrBlock, out.AccepterCidrBlock)
	assert.Equal(t, obs.Status, out.Status)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}

func TestValidateSpec(t *testing.T) {
	err := validateSpec(VPCPeeringSpec{
		Region:         "us-east-1",
		RequesterVpcId: "vpc-a",
		AccepterVpcId:  "vpc-b",
	}, "us-east-1")
	assert.NoError(t, err)

	err = validateSpec(VPCPeeringSpec{Region: "us-east-1", RequesterVpcId: "vpc-a", AccepterVpcId: "vpc-a"}, "us-east-1")
	assert.ErrorContains(t, err, "must be different")
}
