package eip

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_NoDrift(t *testing.T) {
	assert.False(t, HasDrift(
		ElasticIPSpec{Tags: map[string]string{"Name": "web", "env": "dev"}},
		ObservedState{Tags: map[string]string{"Name": "web", "env": "dev", "praxis:managed-key": "us-east-1~web"}},
	))
}

func TestHasDrift_TagAdded(t *testing.T) {
	assert.True(t, HasDrift(
		ElasticIPSpec{Tags: map[string]string{"Name": "web", "env": "dev"}},
		ObservedState{Tags: map[string]string{"Name": "web"}},
	))
}

func TestHasDrift_TagRemoved(t *testing.T) {
	assert.True(t, HasDrift(
		ElasticIPSpec{Tags: map[string]string{"Name": "web"}},
		ObservedState{Tags: map[string]string{"Name": "web", "env": "dev"}},
	))
}

func TestHasDrift_TagChanged(t *testing.T) {
	assert.True(t, HasDrift(
		ElasticIPSpec{Tags: map[string]string{"env": "prod"}},
		ObservedState{Tags: map[string]string{"env": "dev"}},
	))
}

func TestComputeFieldDiffs_Tags(t *testing.T) {
	diffs := ComputeFieldDiffs(
		ElasticIPSpec{Tags: map[string]string{"Name": "web", "env": "prod"}},
		ObservedState{Tags: map[string]string{"Name": "web", "owner": "alice"}},
	)

	assert.Contains(t, diffs, FieldDiffEntry{Path: "tags.env", OldValue: nil, NewValue: "prod"})
	assert.Contains(t, diffs, FieldDiffEntry{Path: "tags.owner", OldValue: "alice", NewValue: nil})
}
