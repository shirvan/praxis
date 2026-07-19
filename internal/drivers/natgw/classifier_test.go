package natgw

import (
	"errors"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
)

func TestClassifyNATErrors(t *testing.T) {
	tests := []struct {
		name       string
		classifier func(error) error
		err        error
		code       int
	}{
		{name: "create validation", classifier: classifyNATCreate, err: &mockAPIError{code: "InvalidParameterValue"}, code: 400},
		{name: "create subnet not found", classifier: classifyNATCreate, err: &mockAPIError{code: "InvalidSubnetID.NotFound"}, code: 400},
		{name: "create allocation conflict", classifier: classifyNATCreate, err: &mockAPIError{code: "Resource.AlreadyAssociated"}, code: 409},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.classifier(tt.err)
			assert.True(t, restate.IsTerminalError(got))
			assert.EqualValues(t, tt.code, restate.ErrorCode(got))
		})
	}

	terminal := restate.TerminalError(errors.New("already terminal"), 418)
	assert.Same(t, terminal, classifyNATCreate(terminal))
	assert.Same(t, terminal, classifyNATMutation(terminal))

	notFound := &mockAPIError{code: "NatGatewayNotFound"}
	assert.Same(t, notFound, classifyNATMutation(notFound))
}
