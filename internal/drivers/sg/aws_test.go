package sg

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

// mockAPIError implements smithy.APIError for testing error classification.
type mockAPIError struct {
	code    string
	message string
}

func (e *mockAPIError) Error() string                 { return fmt.Sprintf("%s: %s", e.code, e.message) }
func (e *mockAPIError) ErrorCode() string             { return e.code }
func (e *mockAPIError) ErrorMessage() string          { return e.message }
func (e *mockAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

func TestIsNotFound_MatchesAPIErrorCode(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidGroup.NotFound"}))
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidGroupId.Malformed"}))
}

func TestIsNotFound_False(t *testing.T) {
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(errors.New("network timeout")))
	assert.False(t, IsNotFound(&mockAPIError{code: "InvalidParameterValue"}))
}

func TestIsDuplicate_MatchesAPIErrorCode(t *testing.T) {
	assert.True(t, IsDuplicate(&mockAPIError{code: "InvalidGroup.Duplicate"}))
}

func TestIsDuplicate_False(t *testing.T) {
	assert.False(t, IsDuplicate(nil))
	assert.False(t, IsDuplicate(errors.New("network timeout")))
	assert.False(t, IsDuplicate(&mockAPIError{code: "InvalidGroup.NotFound"}))
}

func TestIsDependencyViolation_MatchesAPIErrorCode(t *testing.T) {
	assert.True(t, IsDependencyViolation(&mockAPIError{code: "DependencyViolation"}))
}

func TestIsDependencyViolation_FalseForOtherErrors(t *testing.T) {
	assert.False(t, IsDependencyViolation(nil))
	assert.False(t, IsDependencyViolation(errors.New("network timeout")))
	assert.False(t, IsDependencyViolation(&mockAPIError{code: "InvalidGroup.NotFound"}))
}

func TestIsInvalidParam_MatchesCodes(t *testing.T) {
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterValue"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidPermission.Malformed"}))
}

func TestIsInvalidParam_False(t *testing.T) {
	assert.False(t, IsInvalidParam(nil))
	assert.False(t, IsInvalidParam(errors.New("timeout")))
	assert.False(t, IsInvalidParam(&mockAPIError{code: "InvalidGroup.NotFound"}))
}
