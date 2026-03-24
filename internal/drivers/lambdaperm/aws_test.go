package lambdaperm

import (
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

func TestPermissionErrorClassifiers(t *testing.T) {
	assert.True(t, IsConflict(&smithy.GenericAPIError{Code: "ResourceConflictException"}))
	assert.True(t, IsThrottled(&smithy.GenericAPIError{Code: "TooManyRequestsException"}))
	assert.False(t, IsConflict(nil))
	assert.False(t, IsThrottled(nil))
}
