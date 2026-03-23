package iamgroup

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
	assert.True(t, IsNotFound(&mockAPIError{code: "NoSuchEntity"}))
	assert.True(t, IsNotFound(errors.New("api error NoSuchEntity: missing")))
}

func TestIsAlreadyExists_True(t *testing.T) {
	assert.True(t, IsAlreadyExists(&mockAPIError{code: "EntityAlreadyExists"}))
	assert.True(t, IsAlreadyExists(errors.New("api error EntityAlreadyExists: exists")))
}

func TestIsDeleteConflict_True(t *testing.T) {
	assert.True(t, IsDeleteConflict(&mockAPIError{code: "DeleteConflict"}))
	assert.True(t, IsDeleteConflict(errors.New("api error DeleteConflict: attached")))
}

func TestIsMalformedPolicy_True(t *testing.T) {
	assert.True(t, IsMalformedPolicy(&mockAPIError{code: "MalformedPolicyDocument"}))
	assert.True(t, IsMalformedPolicy(errors.New("api error MalformedPolicyDocument: invalid json")))
}
