package kmskey

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func inSyncSpecObserved() (KMSKeySpec, ObservedState) {
	spec := KMSKeySpec{
		Region:            "us-east-1",
		Name:              "prod",
		Description:       "prod key",
		KeyUsage:          "ENCRYPT_DECRYPT",
		KeySpec:           "SYMMETRIC_DEFAULT",
		EnableKeyRotation: true,
		Tags:              map[string]string{"env": "prod"},
	}
	observed := ObservedState{
		ARN:               "arn:aws:kms:us-east-1:123456789012:key/abcd",
		KeyID:             "abcd",
		AliasName:         "alias/prod",
		Description:       "prod key",
		KeyUsage:          "ENCRYPT_DECRYPT",
		KeySpec:           "SYMMETRIC_DEFAULT",
		KeyState:          "Enabled",
		Enabled:           true,
		EnableKeyRotation: true,
		Tags:              map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~prod"},
	}
	return spec, observed
}

func TestHasDrift_InSync(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	assert.False(t, HasDrift(spec, observed), "in-sync spec/observed should not drift")
	assert.Empty(t, ComputeFieldDiffs(spec, observed))
}

func TestHasDrift_Description(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.Description = "changed"
	assert.True(t, HasDrift(spec, observed))
	assert.Contains(t, pathsOf(ComputeFieldDiffs(spec, observed)), "spec.description")
}

func TestHasDrift_Rotation(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.EnableKeyRotation = false
	assert.True(t, HasDrift(spec, observed))
	assert.Contains(t, pathsOf(ComputeFieldDiffs(spec, observed)), "spec.enableKeyRotation")
}

func TestHasDrift_Tags(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.Tags = map[string]string{"env": "staging"}
	assert.True(t, HasDrift(spec, observed))
	assert.Contains(t, pathsOf(ComputeFieldDiffs(spec, observed)), "tags.env")
}

func TestComputeFieldDiffs_ImmutableFieldsAnnotated(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.KeyUsage = "SIGN_VERIFY"
	spec.KeySpec = "RSA_2048"
	paths := pathsOf(ComputeFieldDiffs(spec, observed))
	assert.Contains(t, paths, "spec.keyUsage (immutable, requires replacement)")
	assert.Contains(t, paths, "spec.keySpec (immutable, requires replacement)")
	// Immutable-only divergence is not correctable drift.
	assert.False(t, HasDrift(spec, observed), "immutable fields must not report as correctable drift")
}

func pathsOf(diffs []FieldDiffEntry) []string {
	out := make([]string, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, d.Path)
	}
	return out
}
