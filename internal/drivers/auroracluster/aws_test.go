package auroracluster

import (
	"fmt"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

type mockAPIError struct{ code, msg string }

func (e *mockAPIError) Error() string                 { return fmt.Sprintf("%s: %s", e.code, e.msg) }
func (e *mockAPIError) ErrorCode() string             { return e.code }
func (e *mockAPIError) ErrorMessage() string          { return e.msg }
func (e *mockAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

func TestIsNotFound_True(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "DBClusterNotFoundFault"}))
}

func TestIsNotFound_False(t *testing.T) {
	assert.False(t, IsNotFound(&mockAPIError{code: "SomethingElse"}))
}

func TestIsNotFound_Nil(t *testing.T) {
	assert.False(t, IsNotFound(nil))
}

func TestIsAlreadyExists_True(t *testing.T) {
	assert.True(t, IsAlreadyExists(&mockAPIError{code: "DBClusterAlreadyExistsFault"}))
}

func TestIsAlreadyExists_False(t *testing.T) {
	assert.False(t, IsAlreadyExists(&mockAPIError{code: "SomethingElse"}))
}

func TestIsAlreadyExists_Nil(t *testing.T) {
	assert.False(t, IsAlreadyExists(nil))
}

func TestIsInvalidState_Cluster(t *testing.T) {
	assert.True(t, IsInvalidState(&mockAPIError{code: "InvalidDBClusterStateFault"}))
}

func TestIsInvalidState_Instance(t *testing.T) {
	assert.True(t, IsInvalidState(&mockAPIError{code: "InvalidDBInstanceState"}))
}

func TestIsInvalidState_False(t *testing.T) {
	assert.False(t, IsInvalidState(&mockAPIError{code: "SomethingElse"}))
}

func TestIsInvalidState_Nil(t *testing.T) {
	assert.False(t, IsInvalidState(nil))
}

func TestIsInvalidParam_Value(t *testing.T) {
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterValue"}))
}

func TestIsInvalidParam_Combination(t *testing.T) {
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterCombination"}))
}

func TestIsInvalidParam_False(t *testing.T) {
	assert.False(t, IsInvalidParam(&mockAPIError{code: "SomethingElse"}))
}

func TestIsInvalidParam_Nil(t *testing.T) {
	assert.False(t, IsInvalidParam(nil))
}
