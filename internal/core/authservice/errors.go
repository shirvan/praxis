package authservice

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shirvan/praxis/internal/drivers/awserr"
)

// AuthErrorCode is a machine-readable classification for auth failures.
// These codes appear in AuthError.Code and are used to determine HTTP status
// codes, retryability, and user-facing error messages. Each code maps to
// exactly one HTTP status via AuthError.HTTPCode().
type AuthErrorCode string

const (
	// ErrCodeRegistryNil indicates the auth registry was never initialized.
	// Typically a startup configuration error.
	ErrCodeRegistryNil AuthErrorCode = "AUTH_REGISTRY_NIL"

	// ErrCodeNoDefault indicates no default account is configured.
	ErrCodeNoDefault AuthErrorCode = "AUTH_NO_DEFAULT_ACCOUNT"

	// ErrCodeUnknownAccount indicates the requested account alias does not
	// exist in either the Restate state or the bootstrap config. HTTP 404.
	ErrCodeUnknownAccount AuthErrorCode = "AUTH_UNKNOWN_ACCOUNT"

	// ErrCodeMissingCredentials indicates required credential fields are empty
	// (e.g., static source without accessKeyId, role source without roleArn). HTTP 401.
	ErrCodeMissingCredentials AuthErrorCode = "AUTH_MISSING_CREDENTIALS"

	// ErrCodeUnsupportedSource indicates an unknown credential source type. HTTP 400.
	ErrCodeUnsupportedSource AuthErrorCode = "AUTH_UNSUPPORTED_SOURCE"

	// ErrCodeConfigLoad indicates the AWS SDK config could not be loaded.
	// May be retryable if caused by a transient network issue. HTTP 502.
	ErrCodeConfigLoad AuthErrorCode = "AUTH_CONFIG_LOAD_FAILED"

	// ErrCodeAssumeRole indicates the STS AssumeRole call failed.
	// Retryable for throttling; terminal for access denied. HTTP 401.
	ErrCodeAssumeRole AuthErrorCode = "AUTH_ASSUME_ROLE_FAILED"

	// ErrCodeCredentialRetrieval indicates an unexpected failure during
	// credential resolution (e.g., GetCallerIdentity failed). HTTP 401.
	ErrCodeCredentialRetrieval AuthErrorCode = "AUTH_CREDENTIAL_RETRIEVAL_FAILED"

	// ErrCodeAccessDenied indicates AWS explicitly denied access.
	// This is always a terminal (non-retryable) error. HTTP 403.
	ErrCodeAccessDenied AuthErrorCode = "AUTH_ACCESS_DENIED"
)

// AuthError is a structured error for all auth and credential failures.
// It carries machine-readable code, human-readable message, actionable hint,
// and the original cause error for unwrapping. Drivers wrap these in
// Restate TerminalError (for non-retryable) or return directly (for retryable).
type AuthError struct {
	Code    AuthErrorCode
	Account string
	Message string
	Hint    string
	Cause   error
}

func (e *AuthError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s", e.Code, e.Message)
	if e.Account != "" {
		fmt.Fprintf(&b, " (account: %s)", e.Account)
	}
	if e.Cause != nil {
		fmt.Fprintf(&b, ": %s", e.Cause)
	}
	if e.Hint != "" {
		fmt.Fprintf(&b, "\n  hint: %s", e.Hint)
	}
	return b.String()
}

func (e *AuthError) Unwrap() error { return e.Cause }

// IsRetryable returns true if the error may resolve on retry.
// Only ConfigLoad, CredentialRetrieval, and AssumeRole can be retryable,
// and only when the underlying AWS error is a throttling error.
// All other codes (AccessDenied, MissingCredentials, etc.) are terminal.
func (e *AuthError) IsRetryable() bool {
	switch e.Code {
	case ErrCodeConfigLoad, ErrCodeCredentialRetrieval, ErrCodeAssumeRole:
		if e.Cause != nil {
			return isAWSRetryable(e.Cause)
		}
		return true
	default:
		return false
	}
}

// HTTPCode returns the appropriate HTTP status code for this error.
// Used as the Restate TerminalError code, which propagates to the caller
// and ultimately to the CLI's exit code mapping.
func (e *AuthError) HTTPCode() uint16 {
	switch e.Code {
	case ErrCodeAccessDenied:
		return 403
	case ErrCodeMissingCredentials, ErrCodeAssumeRole, ErrCodeCredentialRetrieval:
		return 401
	case ErrCodeUnknownAccount:
		return 404
	case ErrCodeRegistryNil, ErrCodeNoDefault, ErrCodeUnsupportedSource:
		return 400
	case ErrCodeConfigLoad:
		return 502
	default:
		return 500
	}
}

// IsAuthError returns true if the error chain contains an *AuthError.
func IsAuthError(err error) bool {
	var authErr *AuthError
	return errors.As(err, &authErr)
}

// AsAuthError extracts an *AuthError from the error chain.
func AsAuthError(err error) (*AuthError, bool) {
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return authErr, true
	}
	return nil, false
}

func errRegistryNil() *AuthError {
	return &AuthError{
		Code:    ErrCodeRegistryNil,
		Message: "account registry is not configured",
		Hint:    "Ensure PRAXIS_ACCOUNT_* environment variables are set, or inject an auth registry at startup.",
	}
}

func errUnknownAccount(name string) *AuthError {
	return &AuthError{
		Code:    ErrCodeUnknownAccount,
		Account: name,
		Message: fmt.Sprintf("unknown account %q", name),
		Hint:    fmt.Sprintf("Register account %q via AuthService/%s/Configure.", name, name),
	}
}

func errMissingStaticCredentials(account string) *AuthError {
	return &AuthError{
		Code:    ErrCodeMissingCredentials,
		Account: account,
		Message: fmt.Sprintf("static credentials incomplete for account %q", account),
		Hint:    "Set accessKeyId and secretAccessKey in the account configuration.",
	}
}

func errMissingRoleARN(account string) *AuthError {
	return &AuthError{
		Code:    ErrCodeMissingCredentials,
		Account: account,
		Message: fmt.Sprintf("role ARN missing for account %q", account),
		Hint:    "Set roleArn in the account configuration.",
	}
}

func errUnsupportedSource(account, source string) *AuthError {
	return &AuthError{
		Code:    ErrCodeUnsupportedSource,
		Account: account,
		Message: fmt.Sprintf("unsupported credential source %q for account %q", source, account),
		Hint:    "Use static, role, or default as the credential source.",
	}
}

func errConfigLoad(account string, cause error) *AuthError {
	return &AuthError{
		Code:    ErrCodeConfigLoad,
		Account: account,
		Message: fmt.Sprintf("failed to load AWS config for account %q", account),
		Cause:   cause,
		Hint:    "Check network connectivity and AWS SDK configuration.",
	}
}

func errAssumeRole(account string, cause error) *AuthError {
	return &AuthError{
		Code:    ErrCodeAssumeRole,
		Account: account,
		Message: fmt.Sprintf("STS AssumeRole failed for account %q", account),
		Cause:   cause,
		Hint:    "Check IAM trust policy and role ARN configuration.",
	}
}

func errCredentialRetrieval(account string, cause error) *AuthError {
	return &AuthError{
		Code:    ErrCodeCredentialRetrieval,
		Account: account,
		Message: fmt.Sprintf("credential retrieval failed for account %q", account),
		Cause:   cause,
		Hint:    "Check credential validity and expiry.",
	}
}

// isAccessDenied delegates to the awserr package for consistent AWS error
// code classification across the codebase.
func isAccessDenied(err error) bool {
	return awserr.IsAccessDenied(err)
}

// isExpiredToken delegates to the awserr package for expired token detection.
func isExpiredToken(err error) bool {
	return awserr.IsExpiredToken(err)
}

// isAWSRetryable checks if an AWS error is a throttling error (always retryable).
func isAWSRetryable(err error) bool {
	return awserr.IsThrottled(err)
}

// ClassifyAWSError wraps an AWS API error with auth context if it's an
// authorization failure. Returns the original error unchanged if not auth-related.
func ClassifyAWSError(err error, account string) error {
	if err == nil {
		return nil
	}
	if isAccessDenied(err) {
		return &AuthError{
			Code:    ErrCodeAccessDenied,
			Account: account,
			Message: fmt.Sprintf("AWS denied access for account %q", account),
			Cause:   err,
			Hint:    "Check IAM policies for the credentials associated with this account.",
		}
	}
	if isExpiredToken(err) {
		return &AuthError{
			Code:    ErrCodeCredentialRetrieval,
			Account: account,
			Message: fmt.Sprintf("credentials expired for account %q", account),
			Cause:   err,
			Hint:    "Session token has expired. The Auth Service will refresh automatically on next call.",
		}
	}
	return err
}
