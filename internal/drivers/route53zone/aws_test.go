package route53zone

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
	assert.True(t, IsNotFound(&mockAPIError{code: "NoSuchHostedZone"}))
}

func TestIsNotFound_False(t *testing.T) {
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(errors.New("some other error")))
}

func TestIsAlreadyExists_True(t *testing.T) {
	assert.True(t, IsAlreadyExists(&mockAPIError{code: "HostedZoneAlreadyExists"}))
}

func TestIsConflict_True(t *testing.T) {
	assert.True(t, IsConflict(&mockAPIError{code: "ConflictingDomainExists"}))
	assert.True(t, IsConflict(&mockAPIError{code: "PriorRequestNotComplete"}))
}

func TestIsInvalidInput_True(t *testing.T) {
	assert.True(t, IsInvalidInput(&mockAPIError{code: "InvalidInput"}))
}

func TestIsNotEmpty_True(t *testing.T) {
	assert.True(t, IsNotEmpty(&mockAPIError{code: "HostedZoneNotEmpty"}))
}

func TestIsNotEmpty_False(t *testing.T) {
	assert.False(t, IsNotEmpty(nil))
	assert.False(t, IsNotEmpty(errors.New("some other error")))
}

func TestNormalizeHostedZoneID(t *testing.T) {
	assert.Equal(t, "Z123", normalizeHostedZoneID("/hostedzone/Z123"))
	assert.Equal(t, "Z123", normalizeHostedZoneID("hostedzone/Z123"))
	assert.Equal(t, "Z123", normalizeHostedZoneID("  Z123  "))
	assert.Equal(t, "Z123", normalizeHostedZoneID("Z123"))
}
