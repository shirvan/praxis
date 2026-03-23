package lambda

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrorClassifiers(t *testing.T) {
	assert.True(t, IsNotFound(errors.New("ResourceNotFoundException: missing")))
	assert.True(t, IsConflict(errors.New("ResourceConflictException: busy")))
	assert.True(t, IsInvalidParameter(errors.New("InvalidParameterValueException: bad")))
	assert.True(t, IsAccessDenied(errors.New("AccessDeniedException: denied")))
	assert.True(t, IsThrottled(errors.New("TooManyRequestsException: slow down")))
}