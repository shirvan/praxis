package vpc

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

// mockAPIError implements smithy.APIError for testing error classification.
type mockAPIError struct {
	code    string
	message string
}

func (e *mockAPIError) Error() string                 { return fmt.Sprintf("%s: %s", e.code, e.message) }
func (e *mockAPIError) ErrorCode() string             { return e.code }
func (e *mockAPIError) ErrorMessage() string          { return e.message }
func (e *mockAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

func TestIsNotFound_MatchesAPIErrorCode(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidVpcID.NotFound"}))
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidVpcID.Malformed"}))
}

func TestIsNotFound_MatchesWrappedErrorText(t *testing.T) {
	err := errors.New("[404] operation error EC2: DescribeVpcs, api error InvalidVpcID.NotFound: The VPC ID does not exist")
	assert.True(t, IsNotFound(err))
}

func TestIsNotFound_False(t *testing.T) {
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(errors.New("network timeout")))
	assert.False(t, IsNotFound(&mockAPIError{code: "InvalidParameterValue"}))
}

func TestIsDependencyViolation_MatchesAPIErrorCode(t *testing.T) {
	assert.True(t, IsDependencyViolation(&mockAPIError{code: "DependencyViolation"}))
}

func TestIsDependencyViolation_FalseForOtherErrors(t *testing.T) {
	assert.False(t, IsDependencyViolation(nil))
	assert.False(t, IsDependencyViolation(errors.New("network timeout")))
	assert.False(t, IsDependencyViolation(&mockAPIError{code: "InvalidVpcID.NotFound"}))
}

func TestIsInvalidParam_MatchesCodes(t *testing.T) {
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterValue"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterCombination"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidVpcRange"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "VpcLimitExceeded"}))
}

func TestIsInvalidParam_False(t *testing.T) {
	assert.False(t, IsInvalidParam(nil))
	assert.False(t, IsInvalidParam(errors.New("timeout")))
	assert.False(t, IsInvalidParam(&mockAPIError{code: "InvalidVpcID.NotFound"}))
}

func TestIsCidrConflict_MatchesCodes(t *testing.T) {
	assert.True(t, IsCidrConflict(&mockAPIError{code: "CidrConflict"}))
	assert.True(t, IsCidrConflict(&mockAPIError{code: "InvalidVpc.Range"}))
}

func TestIsCidrConflict_False(t *testing.T) {
	assert.False(t, IsCidrConflict(nil))
	assert.False(t, IsCidrConflict(errors.New("timeout")))
	assert.False(t, IsCidrConflict(&mockAPIError{code: "InvalidParameterValue"}))
}
