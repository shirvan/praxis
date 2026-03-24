package s3

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

func TestIsBucketNotEmpty_True(t *testing.T) {
	assert.True(t, IsBucketNotEmpty(&mockAPIError{code: "BucketNotEmpty"}))
	assert.False(t, IsBucketNotEmpty(nil))
}

func TestIsBucketLimitExceeded_True(t *testing.T) {
	assert.True(t, IsBucketLimitExceeded(&mockAPIError{code: "TooManyBuckets"}))
	assert.False(t, IsBucketLimitExceeded(nil))
	assert.False(t, IsBucketLimitExceeded(&mockAPIError{code: "BucketNotEmpty"}))
}
