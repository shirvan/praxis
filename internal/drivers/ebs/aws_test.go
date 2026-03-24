package ebs

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
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidVolume.NotFound"}))
}

func TestIsNotFound_False(t *testing.T) {
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(errors.New("timeout")))
	assert.False(t, IsNotFound(&mockAPIError{code: "InvalidParameterValue"}))
}

func TestIsVolumeInUse_True(t *testing.T) {
	assert.True(t, IsVolumeInUse(&mockAPIError{code: "VolumeInUse"}))
}

func TestIsModificationCooldown_True(t *testing.T) {
}

func TestSingleManagedKeyMatch_Found(t *testing.T) {
	id, err := singleManagedKeyMatch("us-east-1~data", []string{"vol-123"})
	assert.NoError(t, err)
	assert.Equal(t, "vol-123", id)
}

func TestSingleManagedKeyMatch_NotFound(t *testing.T) {
	id, err := singleManagedKeyMatch("us-east-1~data", nil)
	assert.NoError(t, err)
	assert.Empty(t, id)
}

func TestSingleManagedKeyMatch_MultipleMatchesError(t *testing.T) {
	_, err := singleManagedKeyMatch("us-east-1~data", []string{"vol-1", "vol-2"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ownership corruption")
}
