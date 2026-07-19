package keypair

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"
	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyKeyPairCreateAndMutationTerminalErrors(t *testing.T) {
	duplicate := &smithy.GenericAPIError{Code: "InvalidKeyPair.Duplicate", Message: "key already exists"}
	classified := classifyKeyPairCreate(duplicate)
	require.Error(t, classified)
	assert.True(t, restate.IsTerminalError(classified))
	assert.EqualValues(t, 409, restate.ErrorCode(classified))

	notFound := &smithy.GenericAPIError{Code: "InvalidKeyPair.NotFound", Message: "key not found"}
	classified = classifyKeyPairMutation(notFound)
	require.Error(t, classified)
	assert.True(t, restate.IsTerminalError(classified))
	assert.EqualValues(t, 404, restate.ErrorCode(classified))
	assert.Same(t, notFound, classifyKeyPairObserve(notFound))

	alreadyTerminal := restate.TerminalError(errors.New("already classified"), 422)
	assert.Same(t, alreadyTerminal, classifyKeyPairCreate(alreadyTerminal))
	assert.Same(t, alreadyTerminal, classifyKeyPairMutation(alreadyTerminal))
}
