package snssub

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
	assert.True(t, IsNotFound(errors.New("NotFoundException: sub not found")))
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

func TestIsSubscriptionLimitExceeded_Nil(t *testing.T) {
	assert.False(t, isSubscriptionLimitExceeded(nil))
}

func TestIsSubscriptionLimitExceeded_ByString(t *testing.T) {
	assert.True(t, isSubscriptionLimitExceeded(errors.New("SubscriptionLimitExceeded: too many")))
}

func TestIsSubscriptionLimitExceeded_Unrelated(t *testing.T) {
	assert.False(t, isSubscriptionLimitExceeded(errors.New("timeout")))
}
