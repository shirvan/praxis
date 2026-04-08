package vpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLateInitVPC_FillsInstanceTenancy(t *testing.T) {
	spec := VPCSpec{CidrBlock: "10.0.0.0/16"}
	observed := ObservedState{InstanceTenancy: "default"}

	updated, changed := LateInitVPC(spec, observed)

	assert.True(t, changed)
	assert.Equal(t, "default", updated.InstanceTenancy)
}

func TestLateInitVPC_PreservesExplicitTenancy(t *testing.T) {
	spec := VPCSpec{CidrBlock: "10.0.0.0/16", InstanceTenancy: "dedicated"}
	observed := ObservedState{InstanceTenancy: "default"}

	updated, changed := LateInitVPC(spec, observed)

	assert.False(t, changed)
	assert.Equal(t, "dedicated", updated.InstanceTenancy)
}

func TestLateInitVPC_NoObservedTenancy(t *testing.T) {
	spec := VPCSpec{CidrBlock: "10.0.0.0/16"}
	observed := ObservedState{}

	_, changed := LateInitVPC(spec, observed)

	assert.False(t, changed)
}

func TestLateInitVPC_NoChangeNeeded(t *testing.T) {
	spec := VPCSpec{CidrBlock: "10.0.0.0/16", InstanceTenancy: "default"}
	observed := ObservedState{InstanceTenancy: "default"}

	updated, changed := LateInitVPC(spec, observed)

	assert.False(t, changed)
	assert.Equal(t, "default", updated.InstanceTenancy)
}
