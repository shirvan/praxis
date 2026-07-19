package alb

import (
	"errors"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
)

func TestClassifyALBMutation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
	}{
		{name: "validation", err: &mockAPIError{code: "ValidationError"}, code: 400},
		{name: "conflict", err: &mockAPIError{code: "DuplicateLoadBalancerName"}, code: 409},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyALBMutation(tt.err)
			assert.True(t, restate.IsTerminalError(got))
			assert.EqualValues(t, tt.code, restate.ErrorCode(got))
		})
	}

	terminal := restate.TerminalError(errors.New("already terminal"), 418)
	assert.Same(t, terminal, classifyALBMutation(terminal))
}
