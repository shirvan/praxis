package ami

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewAMIDriver(nil)
	assert.Equal(t, "AMI", drv.ServiceName())
}

func TestValidateSource(t *testing.T) {
	assert.Error(t, validateSource(SourceSpec{}))
	assert.Error(t, validateSource(SourceSpec{FromSnapshot: &FromSnapshotSpec{SnapshotId: "snap-1"}, FromAMI: &FromAMISpec{SourceImageId: "ami-1"}}))
	assert.NoError(t, validateSource(SourceSpec{FromSnapshot: &FromSnapshotSpec{SnapshotId: "snap-1"}}))
	assert.NoError(t, validateSource(SourceSpec{FromAMI: &FromAMISpec{SourceImageId: "ami-1"}}))
}

func TestOutputsFromObserved(t *testing.T) {
	obs := ObservedState{
		ImageId:            "ami-123",
		Name:               "web-ami",
		State:              "available",
		Architecture:       "x86_64",
		VirtualizationType: "hvm",
		RootDeviceName:     "/dev/xvda",
		OwnerId:            "123456789012",
		CreationDate:       "2026-01-01T00:00:00Z",
	}
	out := outputsFromObserved(obs)
	assert.Equal(t, obs.ImageId, out.ImageId)
	assert.Equal(t, obs.Name, out.Name)
	assert.Equal(t, obs.State, out.State)
	assert.Equal(t, obs.Architecture, out.Architecture)
	assert.Equal(t, obs.VirtualizationType, out.VirtualizationType)
	assert.Equal(t, obs.RootDeviceName, out.RootDeviceName)
	assert.Equal(t, obs.OwnerId, out.OwnerId)
	assert.Equal(t, obs.CreationDate, out.CreationDate)
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		ImageId:            "ami-123",
		Name:               "web-ami",
		Description:        "golden image",
		Tags:               map[string]string{"Name": "web-ami", "env": "dev", "praxis:managed-key": "k"},
		LaunchPermAccounts: []string{"111"},
		LaunchPermPublic:   true,
		DeprecationTime:    "2026-12-31T00:00:00Z",
	}
	spec := specFromObserved(obs)
	assert.Equal(t, "web-ami", spec.Name)
	assert.Equal(t, "golden image", spec.Description)
	assert.Equal(t, "ami-123", spec.Source.FromAMI.SourceImageId)
	assert.Equal(t, map[string]string{"Name": "web-ami", "env": "dev"}, spec.Tags)
	assert.Equal(t, []string{"111"}, spec.LaunchPermissions.AccountIds)
	assert.True(t, spec.LaunchPermissions.Public)
	assert.Equal(t, "2026-12-31T00:00:00Z", spec.Deprecation.DeprecateAt)
}

func TestDefaultAMIImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultAMIImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultAMIImportMode(types.ModeManaged))
}

func TestLooksLikeAMIID(t *testing.T) {
	assert.True(t, looksLikeAMIID("ami-0123456789abcdef0"))
	assert.False(t, looksLikeAMIID("web-ami"))
}
