package nacl

import (
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

func TestIsNotFound_True(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidNetworkAclID.NotFound"}))
}

func TestIsInUse_True(t *testing.T) {
	assert.True(t, IsInUse(&mockAPIError{code: "DependencyViolation"}))
}

func TestIsDefaultACL_True(t *testing.T) {
	assert.True(t, IsDefaultACL(&mockAPIError{code: "Client.CannotDelete"}))
}

func TestIsDuplicateRule_True(t *testing.T) {
	assert.True(t, IsDuplicateRule(&mockAPIError{code: "NetworkAclEntryAlreadyExists"}))
}

func TestIsRuleNotFound_True(t *testing.T) {
	assert.True(t, IsRuleNotFound(&mockAPIError{code: "InvalidNetworkAclEntry.NotFound"}))
}

func TestIsLimitExceeded_True(t *testing.T) {
	assert.True(t, IsLimitExceeded(&mockAPIError{code: "NetworkAclLimitExceeded"}))
	assert.True(t, IsLimitExceeded(&mockAPIError{code: "RulesPerAclLimitExceeded"}))
	assert.False(t, IsLimitExceeded(nil))
	assert.False(t, IsLimitExceeded(&mockAPIError{code: "InvalidParameterValue"}))
}

func TestSingleManagedKeyMatch(t *testing.T) {
	id, err := singleManagedKeyMatch("vpc-123~public", []string{"acl-123"})
	assert.NoError(t, err)
	assert.Equal(t, "acl-123", id)

	id, err = singleManagedKeyMatch("vpc-123~public", nil)
	assert.NoError(t, err)
	assert.Empty(t, id)

	_, err = singleManagedKeyMatch("vpc-123~public", []string{"acl-1", "acl-2"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ownership corruption")
}

func TestNormalizeProtocol(t *testing.T) {
	protocol, err := normalizeProtocol("tcp")
	assert.NoError(t, err)
	assert.Equal(t, "6", protocol)

	protocol, err = normalizeProtocol("-1")
	assert.NoError(t, err)
	assert.Equal(t, "-1", protocol)

	_, err = normalizeProtocol("bogus")
	assert.Error(t, err)
}
