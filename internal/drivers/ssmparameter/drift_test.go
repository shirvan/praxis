package ssmparameter

import (
	"github.com/shirvan/praxis/internal/drivers"
	"testing"

	"github.com/stretchr/testify/assert"
)

func desiredSpec() SSMParameterSpec {
	return applyDefaults(SSMParameterSpec{
		Region:        "us-east-1",
		ParameterName: "/praxis/dev/db-host",
		Value:         "db.internal",
		Tags:          map[string]string{"env": "dev"},
	})
}

func observedInSync() ObservedState {
	return ObservedState{
		ParameterName: "/praxis/dev/db-host",
		Type:          "String",
		Value:         "db.internal",
		Tier:          "Standard",
		DataType:      "text",
		Tags:          map[string]string{"env": "dev", "praxis:managed-key": "us-east-1~/praxis/dev/db-host"},
	}
}

func TestHasDrift_NoDrift(t *testing.T) {
	assert.False(t, HasDrift(desiredSpec(), observedInSync()))
}

func TestHasDrift_NormalizedDefaults(t *testing.T) {
	observed := observedInSync()
	observed.Tier = ""
	observed.DataType = ""
	assert.False(t, HasDrift(desiredSpec(), observed), "empty observed tier/dataType should match defaults")
}

func TestHasDrift_ValueChanged(t *testing.T) {
	observed := observedInSync()
	observed.Value = "db-old.internal"
	assert.True(t, HasDrift(desiredSpec(), observed))
}

func TestHasDrift_NameChangedRequiresReplacement(t *testing.T) {
	desired := desiredSpec()
	observed := observedInSync()
	observed.ParameterName = "/praxis/different"

	assert.True(t, HasDrift(desired, observed))
	assert.Contains(t, ComputeFieldDiffs(desired, observed), drivers.FieldDiff{
		Path: "spec.parameterName (immutable, requires replacement)", OldValue: "/praxis/different", NewValue: desired.ParameterName,
	})
}

func TestHasDrift_TagChanged(t *testing.T) {
	observed := observedInSync()
	observed.Tags = map[string]string{"env": "prod"}
	assert.True(t, HasDrift(desiredSpec(), observed))
}

func TestHasDrift_DefaultKmsKeyForSecureString(t *testing.T) {
	desired := desiredSpec()
	desired.Type = "SecureString"
	observed := observedInSync()
	observed.Type = "SecureString"
	observed.KmsKeyID = "alias/aws/ssm"
	assert.False(t, HasDrift(desired, observed), "account default key should not count as drift")

	observed.KmsKeyID = "alias/other-key"
	assert.True(t, HasDrift(desired, observed))
}

func TestComputeFieldDiffs_PlainValue(t *testing.T) {
	observed := observedInSync()
	observed.Value = "db-old.internal"
	diffs := ComputeFieldDiffs(desiredSpec(), observed)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.value", diffs[0].Path)
	assert.Equal(t, "db-old.internal", diffs[0].OldValue)
	assert.Equal(t, "db.internal", diffs[0].NewValue)
}

func TestComputeFieldDiffs_SecureStringMasked(t *testing.T) {
	desired := desiredSpec()
	desired.Type = "SecureString"
	desired.Value = "new-secret"
	observed := observedInSync()
	observed.Type = "SecureString"
	observed.Value = "old-secret"
	observed.KmsKeyID = "alias/aws/ssm"

	diffs := ComputeFieldDiffs(desired, observed)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.value", diffs[0].Path)
	assert.Equal(t, sensitivePlaceholder, diffs[0].OldValue, "SecureString values must be masked")
	assert.Equal(t, sensitivePlaceholder, diffs[0].NewValue, "SecureString values must be masked")
}

func TestComputeFieldDiffs_TagDiffs(t *testing.T) {
	desired := desiredSpec()
	desired.Tags = map[string]string{"env": "staging", "team": "platform"}
	diffs := ComputeFieldDiffs(desired, observedInSync())
	paths := make(map[string]bool, len(diffs))
	for _, diff := range diffs {
		paths[diff.Path] = true
	}
	assert.True(t, paths["tags.env"])
	assert.True(t, paths["tags.team"])
}
