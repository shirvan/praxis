package secret

import (
	"github.com/shirvan/praxis/internal/drivers"
	"testing"

	"github.com/stretchr/testify/assert"
)

func desiredSpec() SecretsManagerSecretSpec {
	return applyDefaults(SecretsManagerSecretSpec{
		Region:       "us-east-1",
		Name:         "app/db-password",
		Description:  "database password",
		SecretString: "s3cr3t",
		Tags:         map[string]string{"env": "dev"},
	})
}

func observedInSync() ObservedState {
	return ObservedState{
		Name:         "app/db-password",
		Description:  "database password",
		SecretString: "s3cr3t",
		Tags:         map[string]string{"env": "dev", "praxis:managed-key": "us-east-1~app/db-password"},
	}
}

func TestHasDrift_NoDrift(t *testing.T) {
	assert.False(t, HasDrift(desiredSpec(), observedInSync()))
}

func TestHasDrift_ValueChanged(t *testing.T) {
	observed := observedInSync()
	observed.SecretString = "old-secret"
	assert.True(t, HasDrift(desiredSpec(), observed))
}

func TestHasDrift_NameChangedRequiresReplacement(t *testing.T) {
	desired := desiredSpec()
	observed := observedInSync()
	observed.Name = "different-secret"

	assert.True(t, HasDrift(desired, observed))
	diffs := ComputeFieldDiffs(desired, observed)
	assert.Contains(t, diffs, drivers.FieldDiff{
		Path: "spec.name (immutable, requires replacement)", OldValue: "different-secret", NewValue: desired.Name,
	})
}

func TestHasDrift_DescriptionChanged(t *testing.T) {
	observed := observedInSync()
	observed.Description = "stale"
	assert.True(t, HasDrift(desiredSpec(), observed))
}

func TestHasDrift_TagChanged(t *testing.T) {
	observed := observedInSync()
	observed.Tags = map[string]string{"env": "prod"}
	assert.True(t, HasDrift(desiredSpec(), observed))
}

func TestHasDrift_DefaultKmsKey(t *testing.T) {
	observed := observedInSync()
	observed.KmsKeyID = "alias/aws/secretsmanager"
	assert.False(t, HasDrift(desiredSpec(), observed), "account default key should not count as drift")

	observed.KmsKeyID = "alias/other-key"
	assert.True(t, HasDrift(desiredSpec(), observed))
}

func TestComputeFieldDiffs_ValueMasked(t *testing.T) {
	desired := desiredSpec()
	desired.SecretString = "new-secret"
	observed := observedInSync()
	observed.SecretString = "old-secret"

	diffs := ComputeFieldDiffs(desired, observed)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.secretString", diffs[0].Path)
	assert.Equal(t, sensitivePlaceholder, diffs[0].OldValue, "secret values must be masked")
	assert.Equal(t, sensitivePlaceholder, diffs[0].NewValue, "secret values must be masked")
}

func TestComputeFieldDiffs_DescriptionAndTags(t *testing.T) {
	desired := desiredSpec()
	desired.Description = "updated"
	desired.Tags = map[string]string{"env": "staging", "team": "platform"}
	diffs := ComputeFieldDiffs(desired, observedInSync())

	paths := make(map[string]any, len(diffs))
	for _, diff := range diffs {
		paths[diff.Path] = diff.NewValue
	}
	assert.Equal(t, "updated", paths["spec.description"])
	assert.Equal(t, "staging", paths["tags.env"])
	assert.Equal(t, "platform", paths["tags.team"])
	assert.NotContains(t, paths, "spec.secretString", "unchanged value should not diff")
}
