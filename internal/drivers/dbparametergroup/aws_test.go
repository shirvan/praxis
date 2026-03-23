package dbparametergroup

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

func TestIsNotFound_DBParameterGroup(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "DBParameterGroupNotFoundFault"}))
	assert.True(t, IsNotFound(errors.New("DBParameterGroupNotFoundFault")))
}

func TestIsNotFound_DBClusterParameterGroup(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "DBClusterParameterGroupNotFoundFault"}))
	assert.True(t, IsNotFound(errors.New("DBClusterParameterGroupNotFoundFault")))
}

func TestIsNotFound_False(t *testing.T) {
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(errors.New("timeout")))
}

func TestIsAlreadyExists_DBParameterGroup(t *testing.T) {
	assert.True(t, IsAlreadyExists(&mockAPIError{code: "DBParameterGroupAlreadyExistsFault"}))
	assert.True(t, IsAlreadyExists(&mockAPIError{code: "DBClusterParameterGroupAlreadyExistsFault"}))
	assert.True(t, IsAlreadyExists(&mockAPIError{code: "DBParameterGroupQuotaExceededFault"}))
}

func TestIsAlreadyExists_StringFallback(t *testing.T) {
	assert.True(t, IsAlreadyExists(errors.New("DBParameterGroupAlreadyExistsFault")))
	assert.True(t, IsAlreadyExists(errors.New("DBClusterParameterGroupAlreadyExistsFault")))
}

func TestIsAlreadyExists_False(t *testing.T) {
	assert.False(t, IsAlreadyExists(nil))
	assert.False(t, IsAlreadyExists(errors.New("timeout")))
}

func TestIsInvalidState_True(t *testing.T) {
	assert.True(t, IsInvalidState(&mockAPIError{code: "InvalidDBParameterGroupStateFault"}))
	assert.True(t, IsInvalidState(&mockAPIError{code: "InvalidDBClusterParameterGroupStateFault"}))
	assert.True(t, IsInvalidState(errors.New("InvalidDBParameterGroupStateFault")))
}

func TestIsInvalidState_False(t *testing.T) {
	assert.False(t, IsInvalidState(nil))
	assert.False(t, IsInvalidState(errors.New("other")))
}

func TestIsInvalidParam_True(t *testing.T) {
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterValue"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterCombination"}))
	assert.True(t, IsInvalidParam(errors.New("InvalidParameterValue: bad")))
}

func TestIsInvalidParam_False(t *testing.T) {
	assert.False(t, IsInvalidParam(nil))
	assert.False(t, IsInvalidParam(errors.New("timeout")))
}
