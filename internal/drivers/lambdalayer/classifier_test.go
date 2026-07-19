package lambdalayer

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"
	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyLayerMutationTerminalErrors(t *testing.T) {
	validation := &smithy.GenericAPIError{Code: "InvalidParameterValueException", Message: "invalid layer content"}
	classified := classifyLayerMutation(validation)
	require.Error(t, classified)
	assert.True(t, restate.IsTerminalError(classified))
	assert.EqualValues(t, 400, restate.ErrorCode(classified))

	alreadyTerminal := restate.TerminalError(errors.New("already classified"), 422)
	assert.Same(t, alreadyTerminal, classifyLayerMutation(alreadyTerminal))
	assert.Nil(t, classifyLayerMutation(nil))
}
