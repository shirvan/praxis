package listener

import (
	"errors"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyListenerMutationTerminalizesProviderErrors(t *testing.T) {
	classified := classifyListenerMutation(&mockAPIError{code: "DuplicateListener"})
	require.True(t, restate.IsTerminalError(classified))
	assert.Equal(t, restate.Code(409), restate.ErrorCode(classified))

	alreadyTerminal := restate.TerminalError(errors.New("invalid listener"), 400)
	assert.Same(t, alreadyTerminal, classifyListenerMutation(alreadyTerminal))
}
