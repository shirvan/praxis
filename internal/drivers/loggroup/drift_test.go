package loggroup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_NoDrift(t *testing.T) {
	retention := int32(14)
	desired := LogGroupSpec{RetentionInDays: &retention, KmsKeyID: "kms-1", Tags: map[string]string{"env": "dev"}}
	observed := ObservedState{RetentionInDays: &retention, KmsKeyID: "kms-1", Tags: map[string]string{"env": "dev"}}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_RetentionChanged(t *testing.T) {
	desired := int32(14)
	observed := int32(30)
	assert.True(t, HasDrift(LogGroupSpec{RetentionInDays: &desired}, ObservedState{RetentionInDays: &observed}))
}

func TestComputeFieldDiffs_ImmutableClass(t *testing.T) {
	diffs := ComputeFieldDiffs(LogGroupSpec{LogGroupClass: "INFREQUENT_ACCESS"}, ObservedState{LogGroupClass: "STANDARD"})
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.logGroupClass (immutable, requires replacement)", diffs[0].Path)
}

func TestComputeFieldDiffs_ImmutableClassWhenObservedEmpty(t *testing.T) {
	diffs := ComputeFieldDiffs(LogGroupSpec{LogGroupClass: "STANDARD"}, ObservedState{LogGroupClass: ""})
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.logGroupClass (immutable, requires replacement)", diffs[0].Path)
	assert.Equal(t, "", diffs[0].OldValue)
	assert.Equal(t, "STANDARD", diffs[0].NewValue)
}
