package iaminstanceprofile

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

func TestErrorClassifiersTypedAndFlattened(t *testing.T) {
	tests := []struct {
		name     string
		classify func(error) bool
		code     string
	}{
		{name: "not found", classify: IsNotFound, code: "NoSuchEntity"},
		{name: "already exists", classify: IsAlreadyExists, code: "EntityAlreadyExists"},
		{name: "delete conflict", classify: IsDeleteConflict, code: "DeleteConflict"},
		{name: "limit exceeded", classify: IsLimitExceeded, code: "LimitExceeded"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.True(t, test.classify(&mockAPIError{code: test.code, message: "typed"}))
			assert.True(t, test.classify(fmt.Errorf("journal replay: %w", &mockAPIError{code: test.code, message: "wrapped"})))
			assert.True(t, test.classify(errors.New(test.code+": flattened by replay")))
			assert.False(t, test.classify(errors.New("unrelated provider error")))
			assert.False(t, test.classify(nil))
		})
	}
}
