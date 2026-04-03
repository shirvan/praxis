package esm

import (
	"fmt"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

func TestESMErrorClassifiers(t *testing.T) {
	assert.True(t, IsConflict(&smithy.GenericAPIError{Code: "ResourceConflictException"}))
	assert.False(t, IsConflict(nil))
}

func TestIsNotFound(t *testing.T) {
	assert.True(t, IsNotFound(&smithy.GenericAPIError{Code: "ResourceNotFoundException"}))
	assert.False(t, IsNotFound(&smithy.GenericAPIError{Code: "OtherException"}))
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(fmt.Errorf("plain error")))
}

func TestIsConflict_OtherError(t *testing.T) {
	assert.False(t, IsConflict(&smithy.GenericAPIError{Code: "ResourceNotFoundException"}))
	assert.False(t, IsConflict(fmt.Errorf("plain error")))
}

func TestIsInvalidParameter(t *testing.T) {
	assert.True(t, IsInvalidParameter(&smithy.GenericAPIError{Code: "InvalidParameterValueException"}))
	assert.False(t, IsInvalidParameter(&smithy.GenericAPIError{Code: "ResourceNotFoundException"}))
	assert.False(t, IsInvalidParameter(nil))
	assert.False(t, IsInvalidParameter(fmt.Errorf("plain error")))
}
