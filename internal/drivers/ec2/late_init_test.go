package ec2

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLateInitEC2Instance_FillsVolumeType(t *testing.T) {
	spec := EC2InstanceSpec{
		RootVolume: &RootVolumeSpec{},
	}
	observed := ObservedState{RootVolumeType: "gp2"}

	updated, changed := LateInitEC2Instance(spec, observed)

	assert.True(t, changed)
	assert.Equal(t, "gp2", updated.RootVolume.VolumeType)
}

func TestLateInitEC2Instance_FillsVolumeSize(t *testing.T) {
	spec := EC2InstanceSpec{
		RootVolume: &RootVolumeSpec{VolumeType: "gp3"},
	}
	observed := ObservedState{RootVolumeSizeGiB: 30}

	updated, changed := LateInitEC2Instance(spec, observed)

	assert.True(t, changed)
	assert.Equal(t, int32(30), updated.RootVolume.SizeGiB)
}

func TestLateInitEC2Instance_FillsBoth(t *testing.T) {
	spec := EC2InstanceSpec{
		RootVolume: &RootVolumeSpec{},
	}
	observed := ObservedState{RootVolumeType: "gp3", RootVolumeSizeGiB: 50}

	updated, changed := LateInitEC2Instance(spec, observed)

	assert.True(t, changed)
	assert.Equal(t, "gp3", updated.RootVolume.VolumeType)
	assert.Equal(t, int32(50), updated.RootVolume.SizeGiB)
}

func TestLateInitEC2Instance_PreservesExplicitVolumeType(t *testing.T) {
	spec := EC2InstanceSpec{
		RootVolume: &RootVolumeSpec{VolumeType: "io1"},
	}
	observed := ObservedState{RootVolumeType: "gp2"}

	updated, changed := LateInitEC2Instance(spec, observed)

	assert.False(t, changed)
	assert.Equal(t, "io1", updated.RootVolume.VolumeType)
}

func TestLateInitEC2Instance_PreservesExplicitVolumeSize(t *testing.T) {
	spec := EC2InstanceSpec{
		RootVolume: &RootVolumeSpec{SizeGiB: 100},
	}
	observed := ObservedState{RootVolumeSizeGiB: 30}

	updated, changed := LateInitEC2Instance(spec, observed)

	assert.False(t, changed)
	assert.Equal(t, int32(100), updated.RootVolume.SizeGiB)
}

func TestLateInitEC2Instance_NilRootVolume(t *testing.T) {
	spec := EC2InstanceSpec{}
	observed := ObservedState{RootVolumeType: "gp2", RootVolumeSizeGiB: 30}

	updated, changed := LateInitEC2Instance(spec, observed)

	assert.False(t, changed)
	assert.Nil(t, updated.RootVolume)
}

func TestLateInitEC2Instance_NoObservedData(t *testing.T) {
	spec := EC2InstanceSpec{
		RootVolume: &RootVolumeSpec{},
	}
	observed := ObservedState{}

	_, changed := LateInitEC2Instance(spec, observed)

	assert.False(t, changed)
}
