package snstopic

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

type mockAPIError struct {
	code    string
	message string
}

func (e *mockAPIError) Error() string                 { return fmt.Sprintf("%s: %s", e.code, e.message) }
func (e *mockAPIError) ErrorCode() string             { return e.code }
func (e *mockAPIError) ErrorMessage() string          { return e.message }
func (e *mockAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

func TestIsNotFound_Nil(t *testing.T) {
	assert.False(t, IsNotFound(nil))
}

func TestIsNotFound_GenericError(t *testing.T) {
	assert.False(t, IsNotFound(errors.New("timeout")))
}

func TestIsNotFound_ByString(t *testing.T) {
	assert.True(t, IsNotFound(errors.New("NotFoundException: topic not found")))
}

func TestIsNotFound_NotFoundSubstring(t *testing.T) {
	assert.True(t, IsNotFound(errors.New("something NotFound something")))
}

func TestIsNotFound_Unrelated(t *testing.T) {
	assert.False(t, IsNotFound(&mockAPIError{code: "InvalidParameter"}))
}

func TestIsInvalidParameter_Nil(t *testing.T) {
	assert.False(t, IsInvalidParameter(nil))
}

func TestIsInvalidParameter_GenericError(t *testing.T) {
	assert.False(t, IsInvalidParameter(errors.New("timeout")))
}

func TestIsInvalidParameter_ByString(t *testing.T) {
	assert.True(t, IsInvalidParameter(errors.New("InvalidParameter: bad value")))
}

func TestIsInvalidParameter_Unrelated(t *testing.T) {
	assert.False(t, IsInvalidParameter(errors.New("timeout")))
}

func TestIsAuthError_Nil(t *testing.T) {
	assert.False(t, isAuthError(nil))
}

func TestIsAuthError_ByString(t *testing.T) {
	assert.True(t, isAuthError(errors.New("AuthorizationError: not authorized")))
}

func TestIsAuthError_Unrelated(t *testing.T) {
	assert.False(t, isAuthError(errors.New("timeout")))
}

func TestExtractTopicName_ValidARN(t *testing.T) {
	assert.Equal(t, "my-topic", extractTopicName("arn:aws:sns:us-east-1:123456789012:my-topic"))
}

func TestExtractTopicName_FifoARN(t *testing.T) {
	assert.Equal(t, "my-topic.fifo", extractTopicName("arn:aws:sns:us-east-1:123456789012:my-topic.fifo"))
}

func TestExtractTopicName_ShortString(t *testing.T) {
	assert.Equal(t, "short", extractTopicName("short"))
}
