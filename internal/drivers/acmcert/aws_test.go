package acmcert

import (
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

type mockAPIError struct {
	code string
}

func (e *mockAPIError) Error() string                 { return e.code }
func (e *mockAPIError) ErrorCode() string             { return e.code }
func (e *mockAPIError) ErrorMessage() string          { return e.code }
func (e *mockAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

func TestACMErrorClassifiers(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "ResourceNotFoundException"}))
	assert.True(t, IsInvalidArn(&mockAPIError{code: "InvalidArnException"}))
	assert.True(t, IsInvalidDomain(&mockAPIError{code: "InvalidDomainValidationOptionsException"}))
	assert.True(t, IsInvalidState(&mockAPIError{code: "InvalidStateException"}))
	assert.True(t, IsQuotaExceeded(&mockAPIError{code: "LimitExceededException"}))
	assert.True(t, IsRequestInProgress(&mockAPIError{code: "RequestInProgressException"}))
	assert.True(t, IsConflict(&mockAPIError{code: "ResourceInUseException"}))
}

func TestSingleManagedKeyMatch(t *testing.T) {
	match, err := singleManagedKeyMatch("k", []string{"arn:1"})
	assert.NoError(t, err)
	assert.Equal(t, "arn:1", match)

	match, err = singleManagedKeyMatch("k", nil)
	assert.NoError(t, err)
	assert.Empty(t, match)

	_, err = singleManagedKeyMatch("k", []string{"arn:2", "arn:1"})
	assert.Error(t, err)
}
