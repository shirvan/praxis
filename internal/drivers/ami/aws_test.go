package ami

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
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidAMIID.NotFound"}))
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidAMIID.Unavailable"}))
	assert.False(t, IsNotFound(&mockAPIError{code: "InvalidParameterValue"}))
}

func TestIsInvalidParam(t *testing.T) {
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterValue"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameter"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "MissingParameter"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidAMIID.Malformed"}))
	assert.False(t, IsInvalidParam(&mockAPIError{code: "AMIQuotaExceeded"}))
}

func TestIsSnapshotNotFound(t *testing.T) {
	assert.True(t, IsSnapshotNotFound(&mockAPIError{code: "InvalidSnapshot.NotFound"}))
	assert.False(t, IsSnapshotNotFound(errors.New("timeout")))
}

func TestIsAMIQuotaExceeded(t *testing.T) {
	assert.True(t, IsAMIQuotaExceeded(&mockAPIError{code: "AMIQuotaExceeded"}))
	assert.False(t, IsAMIQuotaExceeded(errors.New("timeout")))
}
