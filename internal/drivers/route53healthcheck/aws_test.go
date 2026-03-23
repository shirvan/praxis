package route53healthcheck

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

func TestIsNotFound_True(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "NoSuchHealthCheck"}))
	assert.True(t, IsNotFound(errors.New("NoSuchHealthCheck: not found")))
}

func TestIsNotFound_False(t *testing.T) {
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(errors.New("some other error")))
}

func TestIsAlreadyExists_True(t *testing.T) {
	assert.True(t, IsAlreadyExists(&mockAPIError{code: "HealthCheckAlreadyExists"}))
	assert.True(t, IsAlreadyExists(errors.New("HealthCheckAlreadyExists: dup")))
}

func TestIsConflict_True(t *testing.T) {
	assert.True(t, IsConflict(&mockAPIError{code: "PriorRequestNotComplete"}))
	assert.True(t, IsConflict(&mockAPIError{code: "HealthCheckVersionMismatch"}))
	assert.True(t, IsConflict(errors.New("HealthCheckVersionMismatch: stale")))
}

func TestIsInvalidInput_True(t *testing.T) {
	assert.True(t, IsInvalidInput(&mockAPIError{code: "InvalidInput"}))
	assert.True(t, IsInvalidInput(errors.New("InvalidInput: bad value")))
}

func TestIsInvalidInput_False(t *testing.T) {
	assert.False(t, IsInvalidInput(nil))
	assert.False(t, IsInvalidInput(errors.New("some other error")))
}
