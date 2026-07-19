package auroracluster

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/smithy-go"
	restate "github.com/restatedev/sdk-go"
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

func TestClassifyClusterMutation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code restate.Code
	}{
		{
			name: "validation",
			err:  &mockAPIError{code: "InvalidParameterValue"},
			code: 400,
		},
		{
			name: "conflict",
			err:  &mockAPIError{code: "DBClusterAlreadyExistsFault"},
			code: 409,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyClusterMutation(tt.err)
			assert.True(t, restate.IsTerminalError(got))
			assert.Equal(t, tt.code, restate.ErrorCode(got))
		})
	}

	terminal := restate.TerminalError(errors.New("already classified"), 422)
	got := classifyClusterMutation(terminal)
	assert.Same(t, terminal, got, "already-terminal error must be returned unchanged")
	assert.Equal(t, restate.Code(422), restate.ErrorCode(got))
}
