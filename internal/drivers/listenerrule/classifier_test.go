package listenerrule

import (
	"errors"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
)

func TestClassifyListenerRuleMutation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
	}{
		{name: "target group not found", err: &mockAPIError{code: "TargetGroupNotFound"}, code: 400},
		{name: "priority conflict", err: &mockAPIError{code: "PriorityInUse"}, code: 409},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyListenerRuleMutation(tt.err)
			assert.True(t, restate.IsTerminalError(got))
			assert.EqualValues(t, tt.code, restate.ErrorCode(got))
		})
	}

	terminal := restate.TerminalError(errors.New("already terminal"), 418)
	assert.Same(t, terminal, classifyListenerRuleMutation(terminal))
}
