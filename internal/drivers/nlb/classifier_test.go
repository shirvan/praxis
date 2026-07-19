package nlb

import (
	"errors"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
)

func TestClassifyNLBMutation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
	}{
		{name: "validation", err: &mockAPIError{code: "ValidationError"}, code: 400},
		{name: "conflict", err: &mockAPIError{code: "ResourceInUse"}, code: 409},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyNLBMutation(tt.err)
			assert.True(t, restate.IsTerminalError(got))
			assert.EqualValues(t, tt.code, restate.ErrorCode(got))
		})
	}

	terminal := restate.TerminalError(errors.New("already terminal"), 418)
	assert.Same(t, terminal, classifyNLBMutation(terminal))
}
