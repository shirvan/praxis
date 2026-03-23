package esm

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestESMErrorClassifiers(t *testing.T) {
	assert.True(t, IsNotFound(errors.New("ResourceNotFoundException: missing")))
	assert.True(t, IsConflict(errors.New("ResourceConflictException: busy")))
	assert.True(t, IsInvalidParameter(errors.New("InvalidParameterValueException: invalid")))
}
