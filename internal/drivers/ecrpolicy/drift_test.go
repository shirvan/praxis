package ecrpolicy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/internal/drivers/ecrpolicy"
)

func TestHasDrift_NoDrift(t *testing.T) {
	spec := ecrpolicy.ECRLifecyclePolicySpec{
		RepositoryName:      "my-repo",
		LifecyclePolicyText: `{"rules":[{"rulePriority":1}]}`,
	}
	obs := ecrpolicy.ObservedState{
		RepositoryName:      "my-repo",
		LifecyclePolicyText: `{"rules":[{"rulePriority":1}]}`,
	}
	assert.False(t, ecrpolicy.HasDrift(spec, obs))
}

func TestHasDrift_PolicyTextChanged(t *testing.T) {
	spec := ecrpolicy.ECRLifecyclePolicySpec{
		RepositoryName:      "my-repo",
		LifecyclePolicyText: `{"rules":[{"rulePriority":1}]}`,
	}
	obs := ecrpolicy.ObservedState{
		RepositoryName:      "my-repo",
		LifecyclePolicyText: `{"rules":[{"rulePriority":2}]}`,
	}
	assert.True(t, ecrpolicy.HasDrift(spec, obs))
}

func TestHasDrift_PolicyTextJSONNormalized(t *testing.T) {
	// Semantically identical JSON with different whitespace — no drift
	spec := ecrpolicy.ECRLifecyclePolicySpec{
		RepositoryName:      "my-repo",
		LifecyclePolicyText: `{ "rules" : [ { "rulePriority" : 1 } ] }`,
	}
	obs := ecrpolicy.ObservedState{
		RepositoryName:      "my-repo",
		LifecyclePolicyText: `{"rules":[{"rulePriority":1}]}`,
	}
	assert.False(t, ecrpolicy.HasDrift(spec, obs))
}

func TestHasDrift_RepositoryNameImmutable(t *testing.T) {
	// Repository name change is still reported as drift (immutable, ignored)
	spec := ecrpolicy.ECRLifecyclePolicySpec{
		RepositoryName:      "new-repo",
		LifecyclePolicyText: `{"rules":[]}`,
	}
	obs := ecrpolicy.ObservedState{
		RepositoryName:      "old-repo",
		LifecyclePolicyText: `{"rules":[]}`,
	}
	assert.True(t, ecrpolicy.HasDrift(spec, obs))
}

func TestComputeFieldDiffs_NoChanges(t *testing.T) {
	spec := ecrpolicy.ECRLifecyclePolicySpec{
		RepositoryName:      "my-repo",
		LifecyclePolicyText: `{"rules":[]}`,
	}
	obs := ecrpolicy.ObservedState{
		RepositoryName:      "my-repo",
		LifecyclePolicyText: `{"rules":[]}`,
	}
	assert.Empty(t, ecrpolicy.ComputeFieldDiffs(spec, obs))
}

func TestComputeFieldDiffs_PolicyTextChanged(t *testing.T) {
	spec := ecrpolicy.ECRLifecyclePolicySpec{
		RepositoryName:      "my-repo",
		LifecyclePolicyText: `{"rules":[{"rulePriority":1}]}`,
	}
	obs := ecrpolicy.ObservedState{
		RepositoryName:      "my-repo",
		LifecyclePolicyText: `{"rules":[{"rulePriority":2}]}`,
	}
	diffs := ecrpolicy.ComputeFieldDiffs(spec, obs)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.lifecyclePolicyText", diffs[0].Path)
}

func TestComputeFieldDiffs_ImmutableRepositoryName(t *testing.T) {
	spec := ecrpolicy.ECRLifecyclePolicySpec{
		RepositoryName:      "new-repo",
		LifecyclePolicyText: `{"rules":[]}`,
	}
	obs := ecrpolicy.ObservedState{
		RepositoryName:      "old-repo",
		LifecyclePolicyText: `{"rules":[]}`,
	}
	diffs := ecrpolicy.ComputeFieldDiffs(spec, obs)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.repositoryName (immutable, ignored)", diffs[0].Path)
}

func TestComputeFieldDiffs_PolicyTextNormalized(t *testing.T) {
	// Same JSON, different formatting — no diff
	spec := ecrpolicy.ECRLifecyclePolicySpec{
		RepositoryName:      "my-repo",
		LifecyclePolicyText: `{  "rules" : []  }`,
	}
	obs := ecrpolicy.ObservedState{
		RepositoryName:      "my-repo",
		LifecyclePolicyText: `{"rules":[]}`,
	}
	assert.Empty(t, ecrpolicy.ComputeFieldDiffs(spec, obs))
}
