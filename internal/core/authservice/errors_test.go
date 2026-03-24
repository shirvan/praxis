package authservice

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthError_ErrorFormatting(t *testing.T) {
	err := &AuthError{Code: ErrCodeUnknownAccount, Account: "prod", Message: "unknown account", Hint: "Register it first."}
	s := err.Error()
	assert.Contains(t, s, "[AUTH_UNKNOWN_ACCOUNT]")
	assert.Contains(t, s, "unknown account")
	assert.Contains(t, s, "(account: prod)")
	assert.Contains(t, s, "hint: Register it first.")
}

func TestAuthError_ErrorWithCause(t *testing.T) {
	cause := fmt.Errorf("network timeout")
	err := &AuthError{Code: ErrCodeConfigLoad, Account: "dev", Message: "failed to load", Cause: cause}
	assert.Contains(t, err.Error(), "network timeout")
	assert.Contains(t, err.Error(), "[AUTH_CONFIG_LOAD_FAILED]")
}

func TestAuthError_Unwrap(t *testing.T) {
	cause := fmt.Errorf("base error")
	err := &AuthError{Code: ErrCodeAssumeRole, Cause: cause}
	assert.True(t, errors.Is(err, cause))
}

func TestAuthError_IsRetryable(t *testing.T) {
	assert.False(t, (&AuthError{Code: ErrCodeRegistryNil}).IsRetryable())
	assert.True(t, (&AuthError{Code: ErrCodeConfigLoad}).IsRetryable())
	assert.True(t, (&AuthError{Code: ErrCodeCredentialRetrieval}).IsRetryable())
	assert.True(t, (&AuthError{Code: ErrCodeAssumeRole}).IsRetryable())
	assert.False(t, (&AuthError{Code: ErrCodeAccessDenied}).IsRetryable())
}

func TestAuthError_HTTPCode(t *testing.T) {
	assert.Equal(t, uint16(403), (&AuthError{Code: ErrCodeAccessDenied}).HTTPCode())
	assert.Equal(t, uint16(401), (&AuthError{Code: ErrCodeMissingCredentials}).HTTPCode())
	assert.Equal(t, uint16(404), (&AuthError{Code: ErrCodeUnknownAccount}).HTTPCode())
	assert.Equal(t, uint16(400), (&AuthError{Code: ErrCodeRegistryNil}).HTTPCode())
	assert.Equal(t, uint16(502), (&AuthError{Code: ErrCodeConfigLoad}).HTTPCode())
}

func TestIsAuthErrorAndAsAuthError(t *testing.T) {
	authErr := &AuthError{Code: ErrCodeAccessDenied, Account: "prod"}
	wrapped := fmt.Errorf("wrapped: %w", authErr)
	assert.True(t, IsAuthError(wrapped))
	extracted, ok := AsAuthError(wrapped)
	require.True(t, ok)
	assert.Equal(t, ErrCodeAccessDenied, extracted.Code)
	assert.Equal(t, "prod", extracted.Account)
	assert.False(t, IsAuthError(fmt.Errorf("plain error")))
}

func TestErrorConstructors(t *testing.T) {
	assert.Equal(t, ErrCodeRegistryNil, errRegistryNil().Code)
	assert.Equal(t, ErrCodeUnknownAccount, errUnknownAccount("acct").Code)
	assert.Equal(t, ErrCodeMissingCredentials, errMissingStaticCredentials("acct").Code)
	assert.Equal(t, ErrCodeMissingCredentials, errMissingRoleARN("acct").Code)
	assert.Equal(t, ErrCodeUnsupportedSource, errUnsupportedSource("acct", "bad").Code)
	assert.Equal(t, ErrCodeConfigLoad, errConfigLoad("acct", fmt.Errorf("boom")).Code)
	assert.Equal(t, ErrCodeAssumeRole, errAssumeRole("acct", fmt.Errorf("boom")).Code)
	assert.Equal(t, ErrCodeCredentialRetrieval, errCredentialRetrieval("acct", fmt.Errorf("boom")).Code)
}

func TestClassifyAWSError_Passthrough(t *testing.T) {
	err := fmt.Errorf("random error")
	assert.Equal(t, err, ClassifyAWSError(err, "dev"))
	assert.Nil(t, ClassifyAWSError(nil, "dev"))
}
