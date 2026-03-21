package ebs

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewEBSVolumeDriver(nil)
	assert.Equal(t, "EBSVolume", drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		AvailabilityZone: "us-east-1a",
		VolumeType:       "gp3",
		SizeGiB:          100,
		Iops:             3000,
		Throughput:       125,
		Encrypted:        true,
		KmsKeyId:         "kms-1",
		SnapshotId:       "snap-1",
		Tags:             map[string]string{"Name": "data", "praxis:managed-key": "us-east-1~data"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.AvailabilityZone, spec.AvailabilityZone)
	assert.Equal(t, obs.VolumeType, spec.VolumeType)
	assert.Equal(t, obs.SizeGiB, spec.SizeGiB)
	assert.Equal(t, obs.Iops, spec.Iops)
	assert.Equal(t, obs.Throughput, spec.Throughput)
	assert.Equal(t, obs.Encrypted, spec.Encrypted)
	assert.Equal(t, obs.KmsKeyId, spec.KmsKeyId)
	assert.Equal(t, obs.SnapshotId, spec.SnapshotId)
	assert.Equal(t, map[string]string{"Name": "data"}, spec.Tags)
}

func TestDefaultEBSImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultEBSImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultEBSImportMode(types.ModeManaged))
}

func TestVolumeNeedsModification_TypeChange(t *testing.T) {
	assert.True(t, volumeNeedsModification(EBSVolumeSpec{VolumeType: "io2", SizeGiB: 20}, ObservedState{VolumeType: "gp3", SizeGiB: 20}))
}

func TestVolumeNeedsModification_SizeIncrease(t *testing.T) {
	assert.True(t, volumeNeedsModification(EBSVolumeSpec{VolumeType: "gp3", SizeGiB: 40}, ObservedState{VolumeType: "gp3", SizeGiB: 20}))
}

func TestVolumeNeedsModification_NoChange(t *testing.T) {
	assert.False(t, volumeNeedsModification(EBSVolumeSpec{VolumeType: "gp3", SizeGiB: 20, Iops: 3000, Throughput: 125}, ObservedState{VolumeType: "gp3", SizeGiB: 20, Iops: 3000, Throughput: 125}))
}

func TestOutputsFromObserved(t *testing.T) {
	out := outputsFromObserved(ObservedState{
		VolumeId:         "vol-123",
		AvailabilityZone: "us-east-1a",
		State:            "available",
		SizeGiB:          20,
		VolumeType:       "gp3",
		Encrypted:        true,
	}, "us-east-1", "123456789012")
	assert.Equal(t, "vol-123", out.VolumeId)
	assert.Equal(t, "arn:aws:ec2:us-east-1:123456789012:volume/vol-123", out.ARN)
	assert.Equal(t, "us-east-1a", out.AvailabilityZone)
	assert.Equal(t, "available", out.State)
	assert.Equal(t, int32(20), out.SizeGiB)
	assert.Equal(t, "gp3", out.VolumeType)
	assert.True(t, out.Encrypted)
}
