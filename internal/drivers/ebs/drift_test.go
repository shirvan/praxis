package ebs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired := EBSVolumeSpec{VolumeType: "gp3", SizeGiB: 20, Throughput: 125, Encrypted: true, Tags: map[string]string{"Name": "data"}}
	observed := ObservedState{VolumeType: "gp3", SizeGiB: 20, Throughput: 125, Encrypted: true, State: "available", Tags: map[string]string{"Name": "data"}}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_VolumeTypeChanged(t *testing.T) {
	assert.True(t, HasDrift(EBSVolumeSpec{VolumeType: "io2"}, ObservedState{VolumeType: "gp3", State: "available"}))
}

func TestHasDrift_SizeIncreased(t *testing.T) {
	assert.True(t, HasDrift(EBSVolumeSpec{VolumeType: "gp3", SizeGiB: 30}, ObservedState{VolumeType: "gp3", SizeGiB: 20, State: "available"}))
}

func TestHasDrift_IopsChanged(t *testing.T) {
	assert.True(t, HasDrift(EBSVolumeSpec{VolumeType: "io1", SizeGiB: 20, Iops: 3000}, ObservedState{VolumeType: "io1", SizeGiB: 20, Iops: 1000, State: "available"}))
}

func TestHasDrift_ThroughputChanged(t *testing.T) {
	assert.True(t, HasDrift(EBSVolumeSpec{VolumeType: "gp3", SizeGiB: 20, Throughput: 250}, ObservedState{VolumeType: "gp3", SizeGiB: 20, Throughput: 125, State: "available"}))
}

func TestHasDrift_TagsChanged(t *testing.T) {
	desired := EBSVolumeSpec{VolumeType: "gp3", SizeGiB: 20, Tags: map[string]string{"Name": "data", "env": "dev"}}
	observed := ObservedState{VolumeType: "gp3", SizeGiB: 20, State: "available", Tags: map[string]string{"Name": "data"}}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_ImmutableFieldIgnored(t *testing.T) {
	desired := EBSVolumeSpec{AvailabilityZone: "us-east-1b", VolumeType: "gp3", SizeGiB: 20}
	observed := ObservedState{AvailabilityZone: "us-east-1a", VolumeType: "gp3", SizeGiB: 20, State: "available"}
	assert.False(t, HasDrift(desired, observed))
}

func TestComputeFieldDiffs_ShrinkNotSupported(t *testing.T) {
	diffs := ComputeFieldDiffs(EBSVolumeSpec{VolumeType: "gp3", SizeGiB: 10}, ObservedState{VolumeType: "gp3", SizeGiB: 20})
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.sizeGiB (shrink not supported, ignored)", diffs[0].Path)
}

func TestComputeFieldDiffs_ImmutableAZ(t *testing.T) {
	diffs := ComputeFieldDiffs(EBSVolumeSpec{AvailabilityZone: "us-east-1b", VolumeType: "gp3", SizeGiB: 20}, ObservedState{AvailabilityZone: "us-east-1a", VolumeType: "gp3", SizeGiB: 20})
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.availabilityZone (immutable, ignored)", diffs[0].Path)
}
