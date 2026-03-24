package drivers

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/internal/core/authservice"
)

func TestTerminalAuthError_Nil(t *testing.T) {
	assert.Nil(t, TerminalAuthError(nil, "EC2"))
}

func TestTerminalAuthError_NonAuthError(t *testing.T) {
	err := TerminalAuthError(fmt.Errorf("plain error"), "S3Bucket")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "[AUTH] S3Bucket")
}

func TestTerminalAuthError_PermanentAuthError(t *testing.T) {
	authErr := &authservice.AuthError{Code: authservice.ErrCodeUnknownAccount, Account: "prod", Message: "unknown account"}
	err := TerminalAuthError(authErr, "Lambda")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "[AUTH] Lambda")
}

func TestTerminalAuthError_RetryableAuthError(t *testing.T) {
	authErr := &authservice.AuthError{Code: authservice.ErrCodeConfigLoad, Account: "dev", Message: "config load failed"}
	err := TerminalAuthError(authErr, "VPC")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "[AUTH] VPC")
}

func TestClassifyAPIError_Passthrough(t *testing.T) {
	plain := fmt.Errorf("connection refused")
	assert.Equal(t, plain, ClassifyAPIError(plain, "dev", "S3Bucket"))
	assert.Nil(t, ClassifyAPIError(nil, "dev", "EC2"))
}
