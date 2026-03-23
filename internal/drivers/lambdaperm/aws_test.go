package lambdaperm

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPermissionErrorClassifiers(t *testing.T) {
	assert.True(t, IsNotFound(errors.New("ResourceNotFoundException: missing")))
	assert.True(t, IsConflict(errors.New("ResourceConflictException: exists")))
	assert.True(t, IsPreconditionFailed(errors.New("PreconditionFailedException: stale")))
	assert.True(t, IsThrottled(errors.New("TooManyRequestsException: slow down")))
}