package awserr

import (
	"errors"
	"slices"
	"strings"

	"github.com/aws/smithy-go"
)

// ErrorCode extracts the AWS error code from any error in the chain.
// Returns empty string if the error is not an AWS API error.
func ErrorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}

// HasCode returns true if the error's AWS error code matches any of the given codes.
func HasCode(err error, codes ...string) bool {
	code := ErrorCode(err)
	if code == "" {
		return false
	}
	return slices.Contains(codes, code)
}

// HasCodePrefix returns true if the error's AWS code starts with any of the prefixes.
// Useful for families like "InvalidParameterValue", "InvalidParameterCombination".
func HasCodePrefix(err error, prefixes ...string) bool {
	code := ErrorCode(err)
	if code == "" {
		return false
	}
	for _, p := range prefixes {
		if strings.HasPrefix(code, p) {
			return true
		}
	}
	return false
}

// IsThrottled returns true for common AWS throttling error codes.
// These are always retryable and should NOT be wrapped in TerminalError.
func IsThrottled(err error) bool {
	return HasCode(err, "Throttling", "ThrottlingException", "RequestLimitExceeded", "TooManyRequestsException")
}

// IsAccessDenied returns true for AWS authorization failure codes.
func IsAccessDenied(err error) bool {
	return HasCode(err,
		"AccessDenied", "AccessDeniedException",
		"UnauthorizedAccess", "AuthorizationError",
		"AuthFailure", "Forbidden",
		"InvalidClientTokenId", "SignatureDoesNotMatch")
}

// IsExpiredToken returns true for AWS token/session expiry codes.
func IsExpiredToken(err error) bool {
	return HasCode(err, "ExpiredToken", "ExpiredTokenException", "RequestExpired", "TokenRefreshRequired")
}
