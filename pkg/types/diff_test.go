package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMaskSensitiveFieldDiffs(t *testing.T) {
	diffs := []FieldDiff{
		{Path: "spec.name", NewValue: "orders"},
		{Path: "spec.secretString", NewValue: "hunter2"},
		{Path: "spec.secretString.nested", OldValue: "old", NewValue: "new"},
		{Path: "spec.tags.env", NewValue: "prod"},
	}
	out := MaskSensitiveFieldDiffs(diffs, []string{"spec.secretString"})

	byPath := map[string]FieldDiff{}
	for _, d := range out {
		byPath[d.Path] = d
	}
	assert.Equal(t, "orders", byPath["spec.name"].NewValue, "non-sensitive field must not be altered")
	assert.Equal(t, SensitiveFieldPlaceholder, byPath["spec.secretString"].NewValue, "exact sensitive path must be masked")
	assert.Equal(t, SensitiveFieldPlaceholder, byPath["spec.secretString.nested"].NewValue, "nested sensitive path must be masked")
	assert.Equal(t, SensitiveFieldPlaceholder, byPath["spec.secretString.nested"].OldValue, "old value must be masked too")
	assert.Equal(t, "prod", byPath["spec.tags.env"].NewValue, "unrelated field must not be masked")
}

func TestMaskJSONPaths(t *testing.T) {
	m := map[string]any{
		"name":         "db-credentials",
		"secretString": "hunter2",
		"nested": map[string]any{
			"password": "p@ss",
			"port":     float64(5432),
		},
		"empty": "",
	}
	MaskJSONPaths(m, []string{"secretString", "nested.password", "empty", "missing.path"})

	assert.Equal(t, "db-credentials", m["name"], "unlisted field must not be masked")
	assert.Equal(t, SensitiveFieldPlaceholder, m["secretString"])
	nested := m["nested"].(map[string]any)
	assert.Equal(t, SensitiveFieldPlaceholder, nested["password"])
	assert.Equal(t, float64(5432), nested["port"], "sibling of a masked field must not be altered")
	assert.Equal(t, "", m["empty"], "empty-string leaves stay empty — masking them would fake a value")
}

func TestMaskJSONPaths_NilAndEmpty(t *testing.T) {
	MaskJSONPaths(nil, []string{"a"})    // must not panic
	MaskJSONPaths(map[string]any{}, nil) // must not panic
	m := map[string]any{"value": "s3cr3t"}
	MaskJSONPaths(m, nil)
	assert.Equal(t, "s3cr3t", m["value"], "no paths means no masking")
}

func TestFilterIgnoredFieldDiffsCanonicalizesSpecPrefix(t *testing.T) {
	diffs := []FieldDiff{
		{Path: "spec.versioning"},
		{Path: "spec.encryption.algorithm"},
		{Path: "tags.env"},
		{Path: "spec.tags.owner"},
		{Path: "spec.region"},
	}

	filtered := FilterIgnoredFieldDiffs(diffs, []string{"versioning", "encryption", "tags.env", "tags.owner"})
	assert.Equal(t, []FieldDiff{{Path: "spec.region"}}, filtered)
}

func TestFieldPathMatchesRequiresSegmentBoundary(t *testing.T) {
	assert.True(t, FieldPathMatches("spec.tags.env", "tags"))
	assert.True(t, FieldPathMatches("tags.env", "tags.env"))
	assert.False(t, FieldPathMatches("spec.tagsExtra", "tags"))
	assert.False(t, FieldPathMatches("spec.tags.environment", "tags.env"))
}
