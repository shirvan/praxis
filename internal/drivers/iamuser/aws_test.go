package iamuser

import (
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
	assert.True(t, IsNotFound(&mockAPIError{code: "NoSuchEntity"}))
}

func TestIsAlreadyExists_True(t *testing.T) {
	assert.True(t, IsAlreadyExists(&mockAPIError{code: "EntityAlreadyExists"}))
}

func TestIsDeleteConflict_True(t *testing.T) {
	assert.True(t, IsDeleteConflict(&mockAPIError{code: "DeleteConflict"}))
}
