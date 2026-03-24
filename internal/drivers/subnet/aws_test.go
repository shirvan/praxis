package subnet

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
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidSubnetID.NotFound"}))
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidSubnetID.Malformed"}))
}

func TestIsDependencyViolation_True(t *testing.T) {
	assert.True(t, IsDependencyViolation(&mockAPIError{code: "DependencyViolation"}))
}

func TestIsInvalidParam_True(t *testing.T) {
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterValue"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterCombination"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidSubnet.Range"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "SubnetLimitExceeded"}))
}

func TestIsCidrConflict_True(t *testing.T) {
	assert.True(t, IsCidrConflict(&mockAPIError{code: "InvalidSubnet.Conflict"}))
}
