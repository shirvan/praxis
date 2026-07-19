package kernel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type policyState struct {
	Name string            `json:"name"`
	Tags map[string]string `json:"tags,omitempty"`
}

func TestHasDriftIgnoringNestedMapField(t *testing.T) {
	desired := policyState{Name: "resource", Tags: map[string]string{"owner": "praxis", "audit": "desired"}}
	observed := policyState{Name: "resource", Tags: map[string]string{"owner": "praxis", "audit": "external"}}
	hasDrift := func(desired, observed policyState) bool {
		return desired.Name != observed.Name || !assert.ObjectsAreEqual(desired.Tags, observed.Tags)
	}

	drift, err := hasDriftIgnoring(hasDrift, desired, observed, []string{"tags.audit"})
	require.NoError(t, err)
	assert.False(t, drift)

	drift, err = hasDriftIgnoring(hasDrift, desired, observed, []string{"tags.owner"})
	require.NoError(t, err)
	assert.True(t, drift)
}

func TestHasDriftIgnoringAbsentObservedMapKey(t *testing.T) {
	desired := policyState{Tags: map[string]string{"external": "declared"}}
	observed := policyState{Tags: map[string]string{}}
	hasDrift := func(desired, observed policyState) bool {
		return !assert.ObjectsAreEqual(desired.Tags, observed.Tags)
	}

	drift, err := hasDriftIgnoring(hasDrift, desired, observed, []string{"tags.external"})
	require.NoError(t, err)
	assert.False(t, drift)
}
