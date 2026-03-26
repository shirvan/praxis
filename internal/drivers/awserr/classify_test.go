package awserr

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

func (e *mockAPIError) Error() string                 { return e.code + ": " + e.message }
func (e *mockAPIError) ErrorCode() string             { return e.code }
func (e *mockAPIError) ErrorMessage() string          { return e.message }
func (e *mockAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

func TestErrorCode(t *testing.T) {
	t.Run("returns code for API error", func(t *testing.T) {
		err := &mockAPIError{code: "InvalidInstanceID.NotFound", message: "not found"}
		assert.Equal(t, "InvalidInstanceID.NotFound", ErrorCode(err))
	})
	t.Run("returns empty for non-API error", func(t *testing.T) {
		err := fmt.Errorf("some random error")
		assert.Equal(t, "", ErrorCode(err))
	})
	t.Run("extracts code from wrapped message text", func(t *testing.T) {
		err := fmt.Errorf("[404] operation error EC2: DescribeVpcs, api error InvalidVpcID.NotFound: VpcID does not exist")
		assert.Equal(t, "InvalidVpcID.NotFound", ErrorCode(err))
	})
	t.Run("returns empty for nil", func(t *testing.T) {
		assert.Equal(t, "", ErrorCode(nil))
	})
}

func TestHasCode(t *testing.T) {
	err := &mockAPIError{code: "BucketNotEmpty", message: "not empty"}
	assert.True(t, HasCode(err, "BucketNotEmpty"))
	assert.True(t, HasCode(err, "Other", "BucketNotEmpty"))
	assert.False(t, HasCode(err, "NotFound"))
	assert.False(t, HasCode(nil, "BucketNotEmpty"))
	assert.False(t, HasCode(fmt.Errorf("plain"), "BucketNotEmpty"))
}

func TestHasCodePrefix(t *testing.T) {
	err := &mockAPIError{code: "InvalidParameterValue", message: "bad param"}
	assert.True(t, HasCodePrefix(err, "InvalidParameter"))
	assert.True(t, HasCodePrefix(err, "Other", "InvalidParameter"))
	assert.False(t, HasCodePrefix(err, "NotFound"))
	assert.False(t, HasCodePrefix(nil, "InvalidParameter"))
}

func TestIsThrottled(t *testing.T) {
	for _, code := range []string{"Throttling", "ThrottlingException", "RequestLimitExceeded", "TooManyRequestsException"} {
		assert.True(t, IsThrottled(&mockAPIError{code: code}), "expected throttled for ", code)
	}
	assert.False(t, IsThrottled(&mockAPIError{code: "NotFound"}))
	assert.False(t, IsThrottled(nil))
}

func TestIsAccessDenied(t *testing.T) {
	for _, code := range []string{"AccessDenied", "AccessDeniedException", "AuthFailure", "Forbidden"} {
		assert.True(t, IsAccessDenied(&mockAPIError{code: code}), "expected access denied for ", code)
	}
	assert.False(t, IsAccessDenied(&mockAPIError{code: "NotFound"}))
}

func TestIsExpiredToken(t *testing.T) {
	for _, code := range []string{"ExpiredToken", "ExpiredTokenException", "RequestExpired", "TokenRefreshRequired"} {
		assert.True(t, IsExpiredToken(&mockAPIError{code: code}), "expected expired for ", code)
	}
	assert.False(t, IsExpiredToken(&mockAPIError{code: "NotFound"}))
}

func TestWrappedError(t *testing.T) {
	inner := &mockAPIError{code: "Throttling", message: "slow down"}
	wrapped := fmt.Errorf("operation failed: %w", inner)
	assert.True(t, IsThrottled(wrapped))
	assert.True(t, HasCode(wrapped, "Throttling"))
}
