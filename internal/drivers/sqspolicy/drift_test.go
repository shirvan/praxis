package sqspolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift(t *testing.T) {
	assert.False(t, HasDrift(
		SQSQueuePolicySpec{Policy: `{"Version":"2012-10-17","Statement":[]}`},
		ObservedState{Policy: `{"Statement":[],"Version":"2012-10-17"}`},
	))
	assert.True(t, HasDrift(
		SQSQueuePolicySpec{Policy: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow"}]}`},
		ObservedState{Policy: `{"Version":"2012-10-17","Statement":[]}`},
	))
}

func TestPoliciesEqual(t *testing.T) {
	assert.True(t, policiesEqual(`{"a":1,"b":2}`, `{"b":2,"a":1}`))
	assert.False(t, policiesEqual("", `{"a":1}`))
	assert.True(t, policiesEqual("not-json", "not-json"))
	assert.False(t, policiesEqual("not-json", "also-not-json"))
}

func TestComputeFieldDiffs(t *testing.T) {
	diffs := ComputeFieldDiffs(
		SQSQueuePolicySpec{Policy: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow"}]}`},
		ObservedState{Policy: `{"Version":"2012-10-17","Statement":[]}`},
	)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.policy", diffs[0].Path)
}
