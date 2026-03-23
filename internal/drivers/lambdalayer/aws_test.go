package lambdalayer

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLayerErrorClassifiers(t *testing.T) {
	assert.True(t, IsNotFound(errors.New("ResourceNotFoundException: missing")))
	assert.True(t, IsInvalidParameter(errors.New("InvalidParameterValueException: bad")))
	assert.True(t, IsConflict(errors.New("ResourceConflictException: busy")))
	assert.True(t, IsPolicyNotFound(errors.New("ResourceNotFoundException: policy missing")))
}
