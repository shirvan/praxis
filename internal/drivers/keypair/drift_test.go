package keypair

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired := KeyPairSpec{Tags: map[string]string{"env": "dev", "Name": "web"}}
	observed := ObservedState{Tags: map[string]string{"Name": "web", "env": "dev"}}

	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_TagAdded(t *testing.T) {
	desired := KeyPairSpec{Tags: map[string]string{"env": "dev", "Name": "web"}}
	observed := ObservedState{Tags: map[string]string{"Name": "web"}}

	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_TagRemoved(t *testing.T) {
	desired := KeyPairSpec{Tags: map[string]string{"Name": "web"}}
	observed := ObservedState{Tags: map[string]string{"Name": "web", "env": "dev"}}

	assert.True(t, HasDrift(desired, observed))
}

func TestComputeFieldDiffs_ImmutableKeyType(t *testing.T) {
	diffs := ComputeFieldDiffs(
		KeyPairSpec{KeyType: "ed25519", Tags: map[string]string{"Name": "web"}},
		ObservedState{KeyType: "rsa", Tags: map[string]string{"Name": "web"}},
	)

	require.Len(t, diffs, 1)
	assert.Equal(t, "spec.keyType (immutable, ignored)", diffs[0].Path)
	assert.Equal(t, "rsa", diffs[0].OldValue)
	assert.Equal(t, "ed25519", diffs[0].NewValue)
}

func TestComputeFieldDiffs_Tags(t *testing.T) {
	diffs := ComputeFieldDiffs(
		KeyPairSpec{Tags: map[string]string{"Name": "web", "env": "prod", "team": "platform"}},
		ObservedState{Tags: map[string]string{"Name": "web", "env": "dev", "owner": "alice"}},
	)

	assert.ElementsMatch(t, []FieldDiffEntry{
		{Path: "tags.env", OldValue: "dev", NewValue: "prod"},
		{Path: "tags.team", OldValue: nil, NewValue: "platform"},
		{Path: "tags.owner", OldValue: "alice", NewValue: nil},
	}, diffs)
}
