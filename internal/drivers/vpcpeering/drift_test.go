package vpcpeering_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/internal/drivers/vpcpeering"
)

func TestHasDrift_NoDrift(t *testing.T) {
	spec := vpcpeering.VPCPeeringSpec{
		Tags:             map[string]string{"Name": "peer", "env": "dev"},
		RequesterOptions: &vpcpeering.PeeringOptions{AllowDnsResolutionFromRemoteVpc: true},
		AccepterOptions:  &vpcpeering.PeeringOptions{AllowDnsResolutionFromRemoteVpc: true},
	}
	obs := vpcpeering.ObservedState{
		Status:           "active",
		Tags:             map[string]string{"Name": "peer", "env": "dev", "praxis:managed-key": "us-east-1~peer"},
		RequesterOptions: &vpcpeering.PeeringOptions{AllowDnsResolutionFromRemoteVpc: true},
		AccepterOptions:  &vpcpeering.PeeringOptions{AllowDnsResolutionFromRemoteVpc: true},
	}
	assert.False(t, vpcpeering.HasDrift(spec, obs))
}

func TestHasDrift_TagChanged(t *testing.T) {
	spec := vpcpeering.VPCPeeringSpec{Tags: map[string]string{"env": "prod"}}
	obs := vpcpeering.ObservedState{Status: "active", Tags: map[string]string{"env": "dev"}}
	assert.True(t, vpcpeering.HasDrift(spec, obs))
}

func TestHasDrift_DNSOptionsChanged(t *testing.T) {
	spec := vpcpeering.VPCPeeringSpec{RequesterOptions: &vpcpeering.PeeringOptions{AllowDnsResolutionFromRemoteVpc: true}}
	obs := vpcpeering.ObservedState{Status: "active", RequesterOptions: &vpcpeering.PeeringOptions{AllowDnsResolutionFromRemoteVpc: false}}
	assert.True(t, vpcpeering.HasDrift(spec, obs))
}

func TestHasDrift_NonActiveSkipped(t *testing.T) {
	spec := vpcpeering.VPCPeeringSpec{Tags: map[string]string{"env": "prod"}}
	obs := vpcpeering.ObservedState{Status: "pending-acceptance", Tags: map[string]string{"env": "dev"}}
	assert.False(t, vpcpeering.HasDrift(spec, obs))
}

func TestComputeFieldDiffs_ImmutableVPCs(t *testing.T) {
	diffs := vpcpeering.ComputeFieldDiffs(
		vpcpeering.VPCPeeringSpec{RequesterVpcId: "vpc-a", AccepterVpcId: "vpc-b"},
		vpcpeering.ObservedState{RequesterVpcId: "vpc-old-a", AccepterVpcId: "vpc-old-b"},
	)
	paths := map[string]bool{}
	for _, diff := range diffs {
		paths[diff.Path] = true
	}
	assert.True(t, paths["spec.requesterVpcId (immutable, requires replacement)"])
	assert.True(t, paths["spec.accepterVpcId (immutable, requires replacement)"])
}

func TestComputeFieldDiffs_IgnoresPraxisTags(t *testing.T) {
	diffs := vpcpeering.ComputeFieldDiffs(
		vpcpeering.VPCPeeringSpec{Tags: map[string]string{"env": "dev"}},
		vpcpeering.ObservedState{Tags: map[string]string{"env": "dev", "praxis:managed-key": "key"}},
	)
	for _, diff := range diffs {
		assert.NotContains(t, diff.Path, "praxis:")
	}
}