package ami_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/internal/drivers/ami"
)

func TestHasDrift_NoDrift(t *testing.T) {
	spec := ami.AMISpec{
		Description:       "golden image",
		LaunchPermissions: &ami.LaunchPermsSpec{AccountIds: []string{"111111111111"}},
		Deprecation:       &ami.DeprecationSpec{DeprecateAt: "2026-12-31T00:00:00Z"},
		Tags:              map[string]string{"Name": "web-ami", "env": "dev"},
	}
	obs := ami.ObservedState{
		State:              "available",
		Description:        "golden image",
		LaunchPermAccounts: []string{"111111111111"},
		DeprecationTime:    "2026-12-31T00:00:00Z",
		Tags:               map[string]string{"Name": "web-ami", "env": "dev", "praxis:managed-key": "us-east-1~web-ami"},
	}
	assert.False(t, ami.HasDrift(spec, obs))
}

func TestHasDrift_TagsChanged(t *testing.T) {
	assert.True(t, ami.HasDrift(
		ami.AMISpec{Tags: map[string]string{"Name": "web-ami", "env": "prod"}},
		ami.ObservedState{State: "available", Tags: map[string]string{"Name": "web-ami", "env": "dev"}},
	))
}

func TestHasDrift_DescriptionChanged(t *testing.T) {
	assert.True(t, ami.HasDrift(
		ami.AMISpec{Description: "new"},
		ami.ObservedState{State: "available", Description: "old"},
	))
}

func TestHasDrift_LaunchPermAccountAdded(t *testing.T) {
	assert.True(t, ami.HasDrift(
		ami.AMISpec{LaunchPermissions: &ami.LaunchPermsSpec{AccountIds: []string{"111", "222"}}},
		ami.ObservedState{State: "available", LaunchPermAccounts: []string{"111"}},
	))
}

func TestHasDrift_PublicChanged(t *testing.T) {
	assert.True(t, ami.HasDrift(
		ami.AMISpec{LaunchPermissions: &ami.LaunchPermsSpec{Public: true}},
		ami.ObservedState{State: "available", LaunchPermPublic: false},
	))
}

func TestHasDrift_DeprecationChanged(t *testing.T) {
	assert.True(t, ami.HasDrift(
		ami.AMISpec{Deprecation: &ami.DeprecationSpec{DeprecateAt: "2026-12-31T00:00:00Z"}},
		ami.ObservedState{State: "available", DeprecationTime: "2026-01-01T00:00:00Z"},
	))
}

func TestComputeFieldDiffs_AllMutableFields(t *testing.T) {
	diffs := ami.ComputeFieldDiffs(
		ami.AMISpec{
			Name:              "web-ami",
			Description:       "new",
			LaunchPermissions: &ami.LaunchPermsSpec{Public: true, AccountIds: []string{"111"}},
			Deprecation:       &ami.DeprecationSpec{DeprecateAt: "2026-12-31T00:00:00Z"},
			Tags:              map[string]string{"Name": "web-ami", "env": "prod"},
			Source: ami.SourceSpec{FromSnapshot: &ami.FromSnapshotSpec{
				Architecture:       "arm64",
				VirtualizationType: "hvm",
				RootDeviceName:     "/dev/xvda",
			}},
		},
		ami.ObservedState{
			Name:               "old-ami",
			Description:        "old",
			LaunchPermAccounts: []string{"222"},
			DeprecationTime:    "",
			Tags:               map[string]string{"Name": "web-ami", "env": "dev", "old": "value"},
			Architecture:       "x86_64",
			VirtualizationType: "paravirtual",
			RootDeviceName:     "/dev/sda1",
		},
	)

	paths := map[string]bool{}
	for _, diff := range diffs {
		paths[diff.Path] = true
	}
	assert.True(t, paths["spec.description"])
	assert.True(t, paths["spec.launchPermissions"])
	assert.True(t, paths["spec.deprecation.deprecateAt"])
	assert.True(t, paths["tags.env"])
	assert.True(t, paths["tags.old"])
	assert.True(t, paths["spec.name (immutable, ignored)"])
	assert.True(t, paths["spec.source.architecture (immutable, ignored)"])
	assert.True(t, paths["spec.source.virtualizationType (immutable, ignored)"])
	assert.True(t, paths["spec.source.rootDeviceName (immutable, ignored)"])
}
