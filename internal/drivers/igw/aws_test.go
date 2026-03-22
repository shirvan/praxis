package igw

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
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidInternetGatewayID.NotFound"}))
	assert.True(t, IsNotFound(errors.New("api error InvalidInternetGatewayID.NotFound: missing")))
}

func TestIsDependencyViolation_True(t *testing.T) {
	assert.True(t, IsDependencyViolation(&mockAPIError{code: "DependencyViolation"}))
}

func TestIsAlreadyAttached_True(t *testing.T) {
	assert.True(t, IsAlreadyAttached(&mockAPIError{code: "Resource.AlreadyAssociated"}))
	assert.True(t, IsAlreadyAttached(errors.New("api error Resource.AlreadyAssociated: already attached")))
}

func TestIsNotAttached_True(t *testing.T) {
	assert.True(t, IsNotAttached(&mockAPIError{code: "Gateway.NotAttached"}))
	assert.True(t, IsNotAttached(errors.New("api error Gateway.NotAttached: not attached")))
}

func TestIsInvalidParam_True(t *testing.T) {
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterValue"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterCombination"}))
}

func TestSingleManagedKeyMatch(t *testing.T) {
	id, err := singleManagedKeyMatch("us-east-1~web-igw", []string{"igw-123"})
	assert.NoError(t, err)
	assert.Equal(t, "igw-123", id)

	id, err = singleManagedKeyMatch("us-east-1~web-igw", nil)
	assert.NoError(t, err)
	assert.Empty(t, id)

	_, err = singleManagedKeyMatch("us-east-1~web-igw", []string{"igw-1", "igw-2"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ownership corruption")
}
