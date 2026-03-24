package authservice

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate_ValidStatic(t *testing.T) {
	cfg := AccountConfig{
		Region:           "us-east-1",
		CredentialSource: "static",
		AccessKeyID:      "AKID",
		SecretAccessKey:  "secret",
	}
	assert.NoError(t, cfg.Validate("dev"))
}

func TestValidate_ValidRole(t *testing.T) {
	cfg := AccountConfig{
		Region:           "us-west-2",
		CredentialSource: "role",
		RoleARN:          "arn:aws:iam::123456789012:role/test",
	}
	assert.NoError(t, cfg.Validate("prod"))
}

func TestValidate_ValidDefault(t *testing.T) {
	cfg := AccountConfig{
		Region:           "eu-west-1",
		CredentialSource: "default",
	}
	assert.NoError(t, cfg.Validate("staging"))
}

func TestValidate_EmptySourceDefaultsToDefault(t *testing.T) {
	cfg := AccountConfig{Region: "us-east-1"}
	assert.NoError(t, cfg.Validate("myaccount"))
}

func TestValidate_InvalidAlias(t *testing.T) {
	cfg := AccountConfig{Region: "us-east-1", CredentialSource: "default"}
	assert.Error(t, cfg.Validate("INVALID"))
	assert.Error(t, cfg.Validate(""))
	assert.Error(t, cfg.Validate("-starts-with-dash"))
}

func TestValidate_StaticMissingAccessKey(t *testing.T) {
	cfg := AccountConfig{Region: "us-east-1", CredentialSource: "static", SecretAccessKey: "secret"}
	err := cfg.Validate("dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accessKeyId")
}

func TestValidate_StaticMissingSecretKey(t *testing.T) {
	cfg := AccountConfig{Region: "us-east-1", CredentialSource: "static", AccessKeyID: "AKID"}
	err := cfg.Validate("dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secretAccessKey")
}

func TestValidate_RoleMissingARN(t *testing.T) {
	cfg := AccountConfig{Region: "us-east-1", CredentialSource: "role"}
	err := cfg.Validate("dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "roleArn")
}

func TestValidate_UnsupportedSource(t *testing.T) {
	cfg := AccountConfig{Region: "us-east-1", CredentialSource: "magic"}
	err := cfg.Validate("dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported credential source")
}

func TestValidate_SessionDurationBounds(t *testing.T) {
	tooShort := AccountConfig{Region: "us-east-1", CredentialSource: "default", SessionDuration: 5 * time.Minute}
	err := tooShort.Validate("dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "below minimum")

	tooLong := AccountConfig{Region: "us-east-1", CredentialSource: "default", SessionDuration: 13 * time.Hour}
	err = tooLong.Validate("dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")

	valid := AccountConfig{Region: "us-east-1", CredentialSource: "default", SessionDuration: 2 * time.Hour}
	assert.NoError(t, valid.Validate("dev"))
}

func TestRedacted_MasksSecrets(t *testing.T) {
	cfg := AccountConfig{
		Region:           "us-east-1",
		CredentialSource: "static",
		AccessKeyID:      "AKID123",
		SecretAccessKey:  "superSecret",
		RoleARN:          "arn:aws:iam::123:role/test",
	}
	redacted := cfg.Redacted()
	assert.Equal(t, "***", redacted.AccessKeyID)
	assert.Equal(t, "***", redacted.SecretAccessKey)
	assert.Equal(t, "arn:aws:iam::123:role/test", redacted.RoleARN)
	assert.Equal(t, "us-east-1", redacted.Region)
}

func TestValidateAlias(t *testing.T) {
	assert.NoError(t, ValidateAlias("dev"))
	assert.NoError(t, ValidateAlias("prod-us"))
	assert.Error(t, ValidateAlias(""))
	assert.Error(t, ValidateAlias("UPPER"))
}

func TestLoadBootstrapFromEnv_Defaults(t *testing.T) {
	for _, key := range []string{
		"PRAXIS_ACCOUNT_NAME", "PRAXIS_ACCOUNT_REGION", "PRAXIS_ACCOUNT_CREDENTIAL_SOURCE",
		"PRAXIS_ACCOUNT_ACCESS_KEY_ID", "PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "PRAXIS_ACCOUNT_ROLE_ARN",
		"PRAXIS_ACCOUNT_EXTERNAL_ID", "AWS_ENDPOINT_URL",
	} {
		t.Setenv(key, "")
	}

	cfg := LoadBootstrapFromEnv()
	require.NotNil(t, cfg)
	require.Contains(t, cfg.Accounts, "default")
	acc := cfg.Accounts["default"]
	assert.Equal(t, "us-east-1", acc.Region)
	assert.Equal(t, "default", acc.CredentialSource)
}

func TestLoadBootstrapFromEnv_CustomValues(t *testing.T) {
	t.Setenv("PRAXIS_ACCOUNT_NAME", "localstack")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "eu-central-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_ENDPOINT_URL", "http://localhost:4566")
	os.Unsetenv("PRAXIS_ACCOUNT_ROLE_ARN")
	os.Unsetenv("PRAXIS_ACCOUNT_EXTERNAL_ID")

	cfg := LoadBootstrapFromEnv()
	require.NotNil(t, cfg)
	require.Contains(t, cfg.Accounts, "localstack")
	acc := cfg.Accounts["localstack"]
	assert.Equal(t, "eu-central-1", acc.Region)
	assert.Equal(t, "static", acc.CredentialSource)
	assert.Equal(t, "test", acc.AccessKeyID)
	assert.Equal(t, "http://localhost:4566", acc.EndpointURL)
}
