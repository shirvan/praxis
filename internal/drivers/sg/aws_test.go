package sg

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsDependencyViolation_MatchesWrappedErrorText(t *testing.T) {
	err := errors.New("[500] operation error EC2: DeleteSecurityGroup, api error DependencyViolation: resource is still in use")
	assert.True(t, IsDependencyViolation(err))
}

func TestIsDependencyViolation_MatchesRestateWrappedPanicText(t *testing.T) {
	err := errors.New("Invocation panicked, returning error to Restate err=\"[500] [500] (500) operation error EC2: DeleteSecurityGroup, api error DependencyViolation: resource is still in use\\nRelated command: run []\"")
	assert.True(t, IsDependencyViolation(err))
}
