package route53record

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
	assert.True(t, IsNotFound(errors.New("record not found in hosted zone")))
}

func TestIsNotFound_False(t *testing.T) {
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(errors.New("some other error")))
}

func TestIsConflict_True(t *testing.T) {
	assert.True(t, IsConflict(&mockAPIError{code: "PriorRequestNotComplete"}))
	assert.True(t, IsConflict(errors.New("api error PriorRequestNotComplete: busy")))
}

func TestIsInvalidInput_True(t *testing.T) {
	assert.True(t, IsInvalidInput(&mockAPIError{code: "InvalidInput"}))
	assert.True(t, IsInvalidInput(&mockAPIError{code: "InvalidChangeBatch"}))
	assert.True(t, IsInvalidInput(errors.New("api error InvalidChangeBatch: bad")))
}

func TestIsInvalidInput_False(t *testing.T) {
	assert.False(t, IsInvalidInput(nil))
	assert.False(t, IsInvalidInput(errors.New("some other error")))
}

func TestParseRecordIdentity_Simple(t *testing.T) {
	identity, err := parseRecordIdentity("Z123~example.com~A")
	assert.NoError(t, err)
	assert.Equal(t, "Z123", identity.HostedZoneId)
	assert.Equal(t, "example.com", identity.Name)
	assert.Equal(t, "A", identity.Type)
	assert.Empty(t, identity.SetIdentifier)
}

func TestParseRecordIdentity_WithSetIdentifier(t *testing.T) {
	identity, err := parseRecordIdentity("Z123~example.com~A~us-east-1")
	assert.NoError(t, err)
	assert.Equal(t, "Z123", identity.HostedZoneId)
	assert.Equal(t, "example.com", identity.Name)
	assert.Equal(t, "A", identity.Type)
	assert.Equal(t, "us-east-1", identity.SetIdentifier)
}

func TestParseRecordIdentity_InvalidTooFewParts(t *testing.T) {
	_, err := parseRecordIdentity("Z123~example.com")
	assert.Error(t, err)
}

func TestParseRecordIdentity_InvalidTooManyParts(t *testing.T) {
	_, err := parseRecordIdentity("Z123~example.com~A~set~extra")
	assert.Error(t, err)
}
