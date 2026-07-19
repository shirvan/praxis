package iaminstanceprofile

import (
	"testing"

	"github.com/shirvan/praxis/internal/drivers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired := IAMInstanceProfileSpec{RoleName: "app-role", Tags: map[string]string{"env": "dev", "Name": "app-profile"}}
	observed := ObservedState{RoleName: "app-role", Tags: map[string]string{"Name": "app-profile", "env": "dev"}}

	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_RoleChanged(t *testing.T) {
	assert.True(t, HasDrift(IAMInstanceProfileSpec{RoleName: "new-role"}, ObservedState{RoleName: "old-role"}))
}

func TestHasDrift_TagChanged(t *testing.T) {
	assert.True(t, HasDrift(
		IAMInstanceProfileSpec{RoleName: "app-role", Tags: map[string]string{"env": "prod"}},
		ObservedState{RoleName: "app-role", Tags: map[string]string{"env": "dev"}},
	))
}

func TestTagsMatch_IgnoresPraxisTags(t *testing.T) {
	assert.True(t, drivers.TagsMatch(
		map[string]string{"env": "dev"},
		map[string]string{"env": "dev", "praxis:managed-key": "ignore"},
	))
}

func TestComputeFieldDiffs_PathImmutable(t *testing.T) {
	diffs := ComputeFieldDiffs(
		IAMInstanceProfileSpec{Path: "/app/", RoleName: "app-role", Tags: map[string]string{"Name": "profile"}},
		ObservedState{Path: "/ops/", RoleName: "app-role", Tags: map[string]string{"Name": "profile"}},
	)

	require.Len(t, diffs, 1)
	assert.Equal(t, "spec.path (immutable, requires replacement)", diffs[0].Path)
}

func TestHasDrift_ImmutableIdentityChanged(t *testing.T) {
	desired := IAMInstanceProfileSpec{InstanceProfileName: "desired", Path: "/desired/", RoleName: "role"}
	observed := ObservedState{InstanceProfileName: "observed", Path: "/observed/", RoleName: "role"}

	assert.True(t, HasDrift(desired, observed))
	assert.ElementsMatch(t, []drivers.FieldDiff{
		{Path: "spec.instanceProfileName (immutable, requires replacement)", OldValue: "observed", NewValue: "desired"},
		{Path: "spec.path (immutable, requires replacement)", OldValue: "/observed/", NewValue: "/desired/"},
	}, ComputeFieldDiffs(desired, observed))
}

func TestComputeFieldDiffs_RoleAndTags(t *testing.T) {
	diffs := ComputeFieldDiffs(
		IAMInstanceProfileSpec{RoleName: "new-role", Tags: map[string]string{"env": "prod", "team": "platform"}},
		ObservedState{RoleName: "old-role", Tags: map[string]string{"env": "dev", "owner": "alice"}},
	)

	assert.ElementsMatch(t, []drivers.FieldDiff{
		{Path: "spec.roleName", OldValue: "old-role", NewValue: "new-role"},
		{Path: "tags.env", OldValue: "dev", NewValue: "prod"},
		{Path: "tags.team", OldValue: nil, NewValue: "platform"},
		{Path: "tags.owner", OldValue: "alice", NewValue: nil},
	}, diffs)
}
