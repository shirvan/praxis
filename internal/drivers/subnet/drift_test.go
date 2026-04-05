package subnet

import (
	"github.com/shirvan/praxis/internal/drivers"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_NoDrift(t *testing.T) {
	spec := SubnetSpec{MapPublicIpOnLaunch: true, Tags: map[string]string{"Name": "public-a", "env": "dev"}}
	obs := ObservedState{State: "available", MapPublicIpOnLaunch: true, Tags: map[string]string{"Name": "public-a", "env": "dev", "praxis:managed-key": "vpc-1~public-a"}}
	assert.False(t, HasDrift(spec, obs))
}

func TestHasDrift_MapPublicIpChanged(t *testing.T) {
	spec := SubnetSpec{MapPublicIpOnLaunch: true}
	obs := ObservedState{State: "available", MapPublicIpOnLaunch: false}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_TagAdded(t *testing.T) {
	spec := SubnetSpec{Tags: map[string]string{"env": "prod"}}
	obs := ObservedState{State: "available", Tags: map[string]string{}}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_PendingSubnetNoDrift(t *testing.T) {
	spec := SubnetSpec{MapPublicIpOnLaunch: true, Tags: map[string]string{"env": "prod"}}
	obs := ObservedState{State: "pending", MapPublicIpOnLaunch: false, Tags: map[string]string{"env": "dev"}}
	assert.False(t, HasDrift(spec, obs))
}

func TestHasDrift_CidrChangedNoDrift(t *testing.T) {
	spec := SubnetSpec{CidrBlock: "10.0.1.0/24"}
	obs := ObservedState{State: "available", CidrBlock: "10.0.2.0/24"}
	assert.False(t, HasDrift(spec, obs))
}

func TestHasDrift_AZChangedNoDrift(t *testing.T) {
	spec := SubnetSpec{AvailabilityZone: "us-east-1a"}
	obs := ObservedState{State: "available", AvailabilityZone: "us-east-1b"}
	assert.False(t, HasDrift(spec, obs))
}

func TestComputeFieldDiffs_MapPublicIP(t *testing.T) {
	diffs := ComputeFieldDiffs(
		SubnetSpec{MapPublicIpOnLaunch: true},
		ObservedState{MapPublicIpOnLaunch: false},
	)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.mapPublicIpOnLaunch", diffs[0].Path)
}

func TestComputeFieldDiffs_ImmutableCidrBlock(t *testing.T) {
	diffs := ComputeFieldDiffs(
		SubnetSpec{CidrBlock: "10.0.1.0/24"},
		ObservedState{CidrBlock: "10.0.2.0/24"},
	)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.cidrBlock (immutable, requires replacement)", diffs[0].Path)
}

func TestComputeFieldDiffs_ImmutableAZ(t *testing.T) {
	diffs := ComputeFieldDiffs(
		SubnetSpec{AvailabilityZone: "us-east-1a"},
		ObservedState{AvailabilityZone: "us-east-1b"},
	)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.availabilityZone (immutable, requires replacement)", diffs[0].Path)
}

func TestTagsMatch_IgnoresPraxisTags(t *testing.T) {
	assert.True(t, drivers.TagsMatch(
		map[string]string{"env": "dev"},
		map[string]string{"env": "dev", "praxis:managed-key": "vpc-1~public-a"},
	))
}
