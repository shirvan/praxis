package loggroup

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

func TestIsNotFound(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "ResourceNotFoundException"}))
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(errors.New("timeout")))
}

func TestIsAlreadyExists(t *testing.T) {
	assert.True(t, IsAlreadyExists(&mockAPIError{code: "ResourceAlreadyExistsException"}))
	assert.False(t, IsAlreadyExists(&mockAPIError{code: "InvalidParameterException"}))
}

func TestIsInvalidParam(t *testing.T) {
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterException"}))
	assert.False(t, IsInvalidParam(&mockAPIError{code: "LimitExceededException"}))
}
