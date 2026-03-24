package ec2

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
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidInstanceID.NotFound"}))
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidInstanceID.Malformed"}))
}

func TestIsNotFound_False(t *testing.T) {
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(errors.New("network timeout")))
	assert.False(t, IsNotFound(&mockAPIError{code: "InvalidParameterValue"}))
}

func TestIsInvalidParam_MatchesAmiNotFound(t *testing.T) {
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidAMIID.NotFound"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidAMIID.Malformed"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidSubnetID.NotFound"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidGroup.NotFound"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterValue"}))
}

func TestIsInvalidParam_MatchesNil(t *testing.T) {
	assert.False(t, IsInvalidParam(nil))
}

func TestIsInvalidParam_False(t *testing.T) {
	assert.False(t, IsInvalidParam(errors.New("timeout")))
	assert.False(t, IsInvalidParam(&mockAPIError{code: "InsufficientInstanceCapacity"}))
}

func TestIsInsufficientCapacity_True(t *testing.T) {
	assert.True(t, IsInsufficientCapacity(&mockAPIError{code: "InsufficientInstanceCapacity"}))
	assert.True(t, IsInsufficientCapacity(&mockAPIError{code: "InstanceLimitExceeded"}))
	assert.True(t, IsInsufficientCapacity(&mockAPIError{code: "Unsupported"}))
}

func TestIsInsufficientCapacity_MatchesNil(t *testing.T) {
	assert.False(t, IsInsufficientCapacity(nil))
}

func TestIsInsufficientCapacity_False(t *testing.T) {
	assert.False(t, IsInsufficientCapacity(errors.New("timeout")))
	assert.False(t, IsInsufficientCapacity(&mockAPIError{code: "InvalidParameterValue"}))
}

func TestIsTerminated(t *testing.T) {
	assert.False(t, IsTerminated(nil))
	assert.True(t, IsTerminated(errors.New("InvalidInstanceID.NotFound")))
	assert.False(t, IsTerminated(errors.New("network error")))
}

func TestBase64Encode(t *testing.T) {
	assert.Equal(t, "aGVsbG8=", base64Encode("hello"))
}

func TestBase64Encode_AlreadyEncodedInput(t *testing.T) {
	// Double-encoding is intentional — see base64Encode doc.
	encoded := base64Encode("aGVsbG8=")
	assert.NotEqual(t, "aGVsbG8=", encoded, "should double-encode, not pass through")
	assert.Equal(t, "YUdWc2JHOD0=", encoded)
}

func TestExtractProfileName(t *testing.T) {
	assert.Equal(t, "MyProfile", extractProfileName("arn:aws:iam::123456789012:instance-profile/MyProfile"))
	assert.Equal(t, "plain-name", extractProfileName("plain-name"))
}
