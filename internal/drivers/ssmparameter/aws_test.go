package ssmparameter

import (
	"testing"

	"github.com/aws/smithy-go"
	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers"
)

type mockAPIError struct{ code string }

func (e *mockAPIError) Error() string                 { return e.code + ": test error" }
func (e *mockAPIError) ErrorCode() string             { return e.code }
func (e *mockAPIError) ErrorMessage() string          { return "test error" }
func (e *mockAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

func TestSSMParameterErrorPredicates(t *testing.T) {
	for _, code := range []string{"ParameterNotFound", "InvalidResourceId"} {
		assert.True(t, IsNotFound(&mockAPIError{code: code}), code)
	}
	assert.True(t, IsAlreadyExists(&mockAPIError{code: "ParameterAlreadyExists"}))
	for _, code := range []string{"ValidationException", "InvalidKeyId", "InvalidAllowedPatternException", "UnsupportedParameterType"} {
		assert.True(t, IsInvalidParam(&mockAPIError{code: code}), code)
	}
	for _, code := range []string{"ParameterLimitExceeded", "ParameterMaxVersionLimitExceeded"} {
		assert.True(t, IsLimitExceeded(&mockAPIError{code: code}), code)
	}
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsInvalidParam(&mockAPIError{code: "InternalServerError"}))
}

func TestSSMParameterMutationClassification(t *testing.T) {
	for _, tt := range []struct {
		code string
		want restate.Code
	}{
		{code: "ValidationException", want: 400},
		{code: "ParameterNotFound", want: 404},
		{code: "ParameterAlreadyExists", want: 409},
		{code: "ParameterLimitExceeded", want: 503},
		{code: "AccessDeniedException", want: 403},
	} {
		t.Run(tt.code, func(t *testing.T) {
			got := drivers.ClassifyAWS(&mockAPIError{code: tt.code}, classifyMutation)
			require.True(t, restate.IsTerminalError(got))
			assert.Equal(t, tt.want, restate.ErrorCode(got))
		})
	}

	terminal := restate.TerminalError(&mockAPIError{code: "AlreadyClassified"}, 422)
	assert.Same(t, terminal, drivers.ClassifyAWS(terminal, classifyMutation))

	throttled := &mockAPIError{code: "ThrottlingException"}
	assert.Same(t, throttled, drivers.ClassifyAWS(throttled, classifyMutation))
}
