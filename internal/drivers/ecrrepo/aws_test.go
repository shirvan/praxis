package ecrrepo

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

func TestIsNotFound_MatchesRepositoryNotFoundException(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "RepositoryNotFoundException"}))
}

func TestIsNotFound_MatchesStringContains(t *testing.T) {
	assert.True(t, IsNotFound(errors.New("repository my-repo not found")))
}

func TestIsNotFound_False(t *testing.T) {
	assert.False(t, IsNotFound(errors.New("network timeout")))
	assert.False(t, IsNotFound(&mockAPIError{code: "InvalidParameterException"}))
}

func TestIsConflict_MatchesAlreadyExists(t *testing.T) {
	assert.True(t, IsConflict(&mockAPIError{code: "RepositoryAlreadyExistsException"}))
}

func TestIsConflict_False(t *testing.T) {
	assert.False(t, IsConflict(errors.New("timeout")))
	assert.False(t, IsConflict(&mockAPIError{code: "RepositoryNotFoundException"}))
}

func TestIsInvalidParameter_MatchesCodes(t *testing.T) {
	assert.True(t, IsInvalidParameter(&mockAPIError{code: "InvalidParameterException"}))
	assert.True(t, IsInvalidParameter(&mockAPIError{code: "ValidationException"}))
	assert.True(t, IsInvalidParameter(&mockAPIError{code: "InvalidTagParameterException"}))
}

func TestIsInvalidParameter_False(t *testing.T) {
	assert.False(t, IsInvalidParameter(errors.New("timeout")))
	assert.False(t, IsInvalidParameter(&mockAPIError{code: "RepositoryNotFoundException"}))
}

func TestIsRepositoryNotEmpty_MatchesCode(t *testing.T) {
	assert.True(t, IsRepositoryNotEmpty(&mockAPIError{code: "RepositoryNotEmptyException"}))
}

func TestIsRepositoryNotEmpty_False(t *testing.T) {
	assert.False(t, IsRepositoryNotEmpty(errors.New("timeout")))
	assert.False(t, IsRepositoryNotEmpty(&mockAPIError{code: "RepositoryNotFoundException"}))
}

func TestIsRepositoryPolicyNotFound_MatchesCode(t *testing.T) {
	assert.True(t, IsRepositoryPolicyNotFound(&mockAPIError{code: "RepositoryPolicyNotFoundException"}))
}

func TestIsRepositoryPolicyNotFound_False(t *testing.T) {
	assert.False(t, IsRepositoryPolicyNotFound(errors.New("timeout")))
	assert.False(t, IsRepositoryPolicyNotFound(&mockAPIError{code: "RepositoryNotFoundException"}))
}
