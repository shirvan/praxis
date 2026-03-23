package dbsubnetgroup

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
	assert.True(t, IsNotFound(&mockAPIError{code: "DBSubnetGroupNotFoundFault"}))
	assert.True(t, IsNotFound(errors.New("api error DBSubnetGroupNotFoundFault: not found")))
}

func TestIsNotFound_False(t *testing.T) {
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(errors.New("timeout")))
	assert.False(t, IsNotFound(&mockAPIError{code: "InvalidSubnet"}))
}

func TestIsAlreadyExists_True(t *testing.T) {
	assert.True(t, IsAlreadyExists(&mockAPIError{code: "DBSubnetGroupAlreadyExistsFault"}))
	assert.True(t, IsAlreadyExists(errors.New("DBSubnetGroupAlreadyExistsFault")))
}

func TestIsAlreadyExists_False(t *testing.T) {
	assert.False(t, IsAlreadyExists(nil))
	assert.False(t, IsAlreadyExists(errors.New("timeout")))
}

func TestIsInvalidState_True(t *testing.T) {
	assert.True(t, IsInvalidState(&mockAPIError{code: "InvalidDBSubnetGroupStateFault"}))
	assert.True(t, IsInvalidState(errors.New("InvalidDBSubnetGroupStateFault")))
}

func TestIsInvalidState_False(t *testing.T) {
	assert.False(t, IsInvalidState(nil))
	assert.False(t, IsInvalidState(errors.New("other error")))
}

func TestIsInvalidParam_True(t *testing.T) {
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidSubnet"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "DBSubnetGroupDoesNotCoverEnoughAZs"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "SubnetAlreadyInUse"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterValue"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterCombination"}))
	assert.True(t, IsInvalidParam(errors.New("InvalidSubnet: bad")))
}

func TestIsInvalidParam_False(t *testing.T) {
	assert.False(t, IsInvalidParam(nil))
	assert.False(t, IsInvalidParam(errors.New("timeout")))
}
