package ecrpolicy

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

func TestIsNotFound_MatchesLifecyclePolicyNotFoundException(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "LifecyclePolicyNotFoundException"}))
}

func TestIsNotFound_MatchesRepositoryNotFoundException(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "RepositoryNotFoundException"}))
}

func TestIsNotFound_MatchesStringContains(t *testing.T) {
	assert.True(t, IsNotFound(errors.New("lifecycle policy not found")))
}

func TestIsNotFound_False(t *testing.T) {
	assert.False(t, IsNotFound(errors.New("network timeout")))
	assert.False(t, IsNotFound(&mockAPIError{code: "InvalidParameterException"}))
}

func TestIsInvalidParameter_MatchesCodes(t *testing.T) {
	assert.True(t, IsInvalidParameter(&mockAPIError{code: "InvalidParameterException"}))
	assert.True(t, IsInvalidParameter(&mockAPIError{code: "ValidationException"}))
}

func TestIsInvalidParameter_False(t *testing.T) {
	assert.False(t, IsInvalidParameter(errors.New("timeout")))
	assert.False(t, IsInvalidParameter(&mockAPIError{code: "LifecyclePolicyNotFoundException"}))
}

func TestIsRepositoryNotFound_MatchesCode(t *testing.T) {
	assert.True(t, IsRepositoryNotFound(&mockAPIError{code: "RepositoryNotFoundException"}))
}

func TestIsRepositoryNotFound_False(t *testing.T) {
	assert.False(t, IsRepositoryNotFound(errors.New("timeout")))
	assert.False(t, IsRepositoryNotFound(&mockAPIError{code: "InvalidParameterException"}))
}
