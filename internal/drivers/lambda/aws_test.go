package lambda

import (
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

func TestErrorClassifiers(t *testing.T) {
	assert.True(t, IsConflict(&smithy.GenericAPIError{Code: "ResourceConflictException"}))
	assert.True(t, IsAccessDenied(&smithy.GenericAPIError{Code: "AccessDeniedException"}))
	assert.False(t, IsConflict(nil))
	assert.False(t, IsAccessDenied(nil))
}
