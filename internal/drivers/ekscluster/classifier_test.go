package ekscluster

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"
	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyEKSCreateAndMutationTerminalErrors(t *testing.T) {
	conflict := &smithy.GenericAPIError{Code: "ResourceInUseException", Message: "cluster already exists"}
	classified := classifyEKSCreate(conflict)
	require.Error(t, classified)
	assert.True(t, restate.IsTerminalError(classified))
	assert.EqualValues(t, 409, restate.ErrorCode(classified))

	notFound := &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "cluster not found"}
	classified = classifyEKSMutation(notFound)
	require.Error(t, classified)
	assert.True(t, restate.IsTerminalError(classified))
	assert.EqualValues(t, 404, restate.ErrorCode(classified))
	assert.Same(t, notFound, classifyEKSDelete(notFound))

	alreadyTerminal := restate.TerminalError(errors.New("already classified"), 422)
	assert.Same(t, alreadyTerminal, classifyEKSCreate(alreadyTerminal))
	assert.Same(t, alreadyTerminal, classifyEKSMutation(alreadyTerminal))
}
