package keypair

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
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidKeyPair.NotFound"}))
	assert.True(t, IsNotFound(errors.New("api error InvalidKeyPair.NotFound: missing")))
}

func TestIsNotFound_False(t *testing.T) {
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(errors.New("timeout")))
	assert.False(t, IsNotFound(&mockAPIError{code: "InvalidParameterValue"}))
}

func TestIsDuplicate_True(t *testing.T) {
	assert.True(t, IsDuplicate(&mockAPIError{code: "InvalidKeyPair.Duplicate"}))
	assert.True(t, IsDuplicate(errors.New("api error InvalidKeyPair.Duplicate: exists")))
}

func TestIsInvalidKeyFormat_True(t *testing.T) {
	assert.True(t, IsInvalidKeyFormat(&mockAPIError{code: "InvalidKey.Format"}))
	assert.True(t, IsInvalidKeyFormat(&mockAPIError{code: "InvalidKeyPair.Format"}))
	assert.True(t, IsInvalidKeyFormat(errors.New("api error InvalidKey.Format: bad key")))
	assert.True(t, IsInvalidKeyFormat(errors.New("api error InvalidKeyPair.Format: bad key")))
	assert.False(t, IsInvalidKeyFormat(errors.New("other error")))
}
