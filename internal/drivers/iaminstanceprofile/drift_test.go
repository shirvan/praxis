package iaminstanceprofile

import (
	"testing"

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
	assert.True(t, tagsMatch(
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
	assert.Equal(t, "spec.path (immutable, ignored)", diffs[0].Path)
}

func TestComputeFieldDiffs_RoleAndTags(t *testing.T) {
	diffs := ComputeFieldDiffs(
		IAMInstanceProfileSpec{RoleName: "new-role", Tags: map[string]string{"env": "prod", "team": "platform"}},
		ObservedState{RoleName: "old-role", Tags: map[string]string{"env": "dev", "owner": "alice"}},
	)

	assert.ElementsMatch(t, []FieldDiffEntry{
		{Path: "spec.roleName", OldValue: "old-role", NewValue: "new-role"},
		{Path: "tags.env", OldValue: "dev", NewValue: "prod"},
		{Path: "tags.team", OldValue: nil, NewValue: "platform"},
		{Path: "tags.owner", OldValue: "alice", NewValue: nil},
	}, diffs)
}
