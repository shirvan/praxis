// Package awserr provides AWS error code extraction and classification helpers.
// All Praxis drivers import this package to determine whether an AWS API error
// is retryable (throttling), terminal (access denied), or informational.
//
// Error code extraction uses two strategies:
//  1. Type assertion to smithy.APIError (preferred, works for typed SDK errors)
//  2. String parsing of error messages (fallback for wrapped/serialized errors)
//
// This dual approach handles cases where errors have been serialized across
// Restate service boundaries and lost their original Go type information.
package awserr

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/smithy-go"
)

// ErrNotFound is a sentinel error that driver Describe helpers return when an
// AWS API call succeeds but the result set is empty (resource deleted externally
// or never existed). Wrapping this sentinel lets IsNotFound recognise the
// condition without relying on string matching.
var ErrNotFound = errors.New("not found")

// ErrorCode extracts the AWS error code from any error in the chain.
// Returns empty string if the error is not an AWS API error.
//
// Two extraction strategies:
//  1. Type assertion: uses errors.As to find a smithy.APIError and read its code.
//  2. String parsing: looks for "api error <CODE>:" in the error message.
//     This handles errors that were serialized (e.g., across Restate boundaries)
//     and no longer carry the original smithy.APIError type.
func ErrorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	message := errString(err)
	if message == "" {
		return ""
	}
	const marker = "api error "
	_, after, found := strings.Cut(message, marker)
	if !found {
		return ""
	}
	code, _, hasColon := strings.Cut(after, ":")
	if !hasColon || strings.TrimSpace(code) == "" {
		return ""
	}
	return strings.TrimSpace(code)
}

// errString safely converts an error to its string representation.
// Returns empty string for nil errors.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// HasCode returns true if the error's AWS error code matches any of the given codes.
//
// When no code can be extracted (typed extraction failed and the message lacks
// the "api error" marker), falls back to scanning the message for "<code>:".
// Modeled AWS errors stringify as "<Code>: <message>", and errors that crossed
// a Restate journal boundary are flattened to plain strings, so this fallback
// is what keeps classification working after replay.
func HasCode(err error, codes ...string) bool {
	if code := ErrorCode(err); code != "" {
		return slices.Contains(codes, code)
	}
	message := errString(err)
	if message == "" {
		return false
	}
	for _, code := range codes {
		if strings.Contains(message, code+":") {
			return true
		}
	}
	return false
}

// NotFound wraps ErrNotFound with a descriptive message. Use this instead of
// bare errors.New("...not found") in driver Describe helpers so that
// IsNotFoundErr can recognise the error.
func NotFound(msg string) error {
	return fmt.Errorf("%s: %w", msg, ErrNotFound)
}

// IsNotFoundErr returns true if any error in the chain wraps ErrNotFound.
//
// Falls back to string matching for errors flattened by the Restate journal:
// NotFound wraps as "%s: not found", so a message that is exactly "not found"
// or contains ": not found" is treated as a not-found condition.
func IsNotFoundErr(err error) bool {
	if errors.Is(err, ErrNotFound) {
		return true
	}
	message := errString(err)
	return message == "not found" || strings.Contains(message, ": not found")
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

// IsValidation reports provider request-shape errors that retries cannot fix.
// It is intentionally narrower than all 4xx AWS errors; callers must classify
// not-found and conflicts using resource-specific semantics.
func IsValidation(err error) bool {
	return HasCode(err,
		"ValidationException", "ValidationError",
		"InvalidRequestException", "SerializationException", "MalformedQueryString") ||
		HasCodePrefix(err, "InvalidParameter")
}

// IsExpiredToken returns true for AWS token/session expiry codes.
func IsExpiredToken(err error) bool {
	return HasCode(err, "ExpiredToken", "ExpiredTokenException", "RequestExpired", "TokenRefreshRequired")
}
