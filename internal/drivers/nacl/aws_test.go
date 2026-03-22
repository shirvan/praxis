package nacl

import (
	"errors"
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
	assert.True(t, IsNotFound(errors.New("api error InvalidNetworkAclID.NotFound: missing")))
}

func TestIsInUse_True(t *testing.T) {
	assert.True(t, IsInUse(&mockAPIError{code: "DependencyViolation"}))
	assert.True(t, IsInUse(errors.New("DependencyViolation: network ACL has dependencies and cannot be deleted")))
}

func TestIsDefaultACL_True(t *testing.T) {
	assert.True(t, IsDefaultACL(&mockAPIError{code: "Client.CannotDelete"}))
	assert.True(t, IsDefaultACL(errors.New("cannot delete default network ACL")))
}

func TestIsDuplicateRule_True(t *testing.T) {
	assert.True(t, IsDuplicateRule(&mockAPIError{code: "NetworkAclEntryAlreadyExists"}))
	assert.True(t, IsDuplicateRule(errors.New("api error NetworkAclEntryAlreadyExists: duplicate")))
}

func TestIsRuleNotFound_True(t *testing.T) {
	assert.True(t, IsRuleNotFound(&mockAPIError{code: "InvalidNetworkAclEntry.NotFound"}))
	assert.True(t, IsRuleNotFound(errors.New("api error InvalidNetworkAclEntry.NotFound: missing")))
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
