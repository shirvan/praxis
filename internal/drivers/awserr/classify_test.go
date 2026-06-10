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

// Errors that cross the Restate journal are flattened to plain strings, losing
// their smithy.APIError type. Modeled AWS errors stringify as "<Code>: <message>"
// inside the operation error text, so HasCode must fall back to scanning for
// "<code>:" when typed extraction yields nothing.
func TestHasCode_FlattenedJournalStrings(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		codes []string
		want  bool
	}{
		{
			name:  "flattened modeled IAM not-found",
			err:   fmt.Errorf("operation error IAM: GetRole, https response error StatusCode: 404, RequestID: x, NoSuchEntity: role not found"),
			codes: []string{"NoSuchEntity"},
			want:  true,
		},
		{
			name:  "flattened modeled S3 bucket conflict",
			err:   fmt.Errorf("operation error S3: CreateBucket, https response error StatusCode: 409, RequestID: x, BucketAlreadyOwnedByYou: bucket exists"),
			codes: []string{"BucketAlreadyOwnedByYou", "BucketAlreadyExists"},
			want:  true,
		},
		{
			name:  "flattened EC2 dotted code",
			err:   fmt.Errorf("operation error EC2: DescribeSecurityGroups, https response error StatusCode: 400, RequestID: x, InvalidGroup.NotFound: group does not exist"),
			codes: []string{"InvalidGroup.NotFound"},
			want:  true,
		},
		{
			name:  "flattened unmodeled generic api error still uses marker path",
			err:   fmt.Errorf("operation error EC2: DescribeVpcs, api error InvalidVpcID.NotFound: vpc does not exist"),
			codes: []string{"InvalidVpcID.NotFound"},
			want:  true,
		},
		{
			name:  "code absent from flattened message",
			err:   fmt.Errorf("operation error S3: HeadBucket, https response error StatusCode: 403, RequestID: x, Forbidden: access denied"),
			codes: []string{"NotFound", "NoSuchBucket"},
			want:  false,
		},
		{
			name:  "typed error code still wins over message scan",
			err:   &mockAPIError{code: "Throttling", message: "mentions NoSuchEntity: in message"},
			codes: []string{"NoSuchEntity"},
			want:  false,
		},
		{
			name:  "nil error",
			err:   nil,
			codes: []string{"NoSuchEntity"},
			want:  false,
		},
		{
			name:  "code without trailing colon does not match",
			err:   fmt.Errorf("the NoSuchEntity code appeared without colon"),
			codes: []string{"NoSuchEntity"},
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, HasCode(tt.err, tt.codes...))
		})
	}
}

func TestIsNotFoundErr_FlattenedJournalStrings(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "wrapped sentinel", err: NotFound("security group sg-123"), want: true},
		{name: "doubly wrapped sentinel", err: fmt.Errorf("describe: %w", NotFound("vpc vpc-123")), want: true},
		{name: "flattened wrap format", err: fmt.Errorf("security group sg-123 not found: not found"), want: true},
		{name: "flattened bare sentinel", err: fmt.Errorf("not found"), want: true},
		{name: "nil", err: nil, want: false},
		{name: "unrelated error", err: fmt.Errorf("some other error"), want: false},
		{name: "not-found words without wrap format", err: fmt.Errorf("role example not found"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsNotFoundErr(tt.err))
		})
	}
}
