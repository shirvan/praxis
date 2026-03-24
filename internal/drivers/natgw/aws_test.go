package natgw

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
	assert.True(t, IsNotFound(&mockAPIError{code: "NatGatewayNotFound"}))
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidNatGatewayID.NotFound"}))
}

func TestIsNotFound_False(t *testing.T) {
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(errors.New("timeout")))
	assert.False(t, IsNotFound(&mockAPIError{code: "InvalidParameterValue"}))
}

func TestIsInvalidParam_True(t *testing.T) {
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterValue"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterCombination"}))
}

func TestIsAllocationInUse_True(t *testing.T) {
	assert.True(t, IsAllocationInUse(&mockAPIError{code: "Resource.AlreadyAssociated"}))
	assert.True(t, IsAllocationInUse(&mockAPIError{code: "InvalidAllocationID.NotFound"}))
}

func TestIsSubnetNotFound_True(t *testing.T) {
	assert.True(t, IsSubnetNotFound(&mockAPIError{code: "InvalidSubnetID.NotFound"}))
}

func TestIsFailed(t *testing.T) {
	assert.True(t, IsFailed("failed"))
	assert.False(t, IsFailed("available"))
}

func TestSingleManagedKeyMatch(t *testing.T) {
	id, err := singleManagedKeyMatch("us-east-1~nat-a", []string{"nat-123"})
	assert.NoError(t, err)
	assert.Equal(t, "nat-123", id)

	id, err = singleManagedKeyMatch("us-east-1~nat-a", nil)
	assert.NoError(t, err)
	assert.Empty(t, id)

	_, err = singleManagedKeyMatch("us-east-1~nat-a", []string{"nat-1", "nat-2"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ownership corruption")
}
