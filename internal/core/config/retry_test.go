package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultRetryPolicy_ReturnsNonNil(t *testing.T) {
	policy := DefaultRetryPolicy()
	assert.NotNil(t, policy, "DefaultRetryPolicy must return a non-nil ServiceDefinitionOption")
}
