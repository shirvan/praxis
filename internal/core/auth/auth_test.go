package auth

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFromEnv_StaticAccount(t *testing.T) {
	t.Setenv("PRAXIS_ACCOUNT_NAME", "dev")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-west-2")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", CredentialSourceStatic)
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test-key")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("AWS_ENDPOINT_URL", "http://localstack:4566")

	registry := LoadFromEnv()
	account, err := registry.Lookup("")
	require.NoError(t, err)

	assert.Equal(t, "dev", account.Name)
	assert.Equal(t, "us-west-2", account.Region)
	assert.Equal(t, CredentialSourceStatic, account.CredentialSource)
	assert.Equal(t, "test-key", account.AccessKeyID)
	assert.Equal(t, "test-secret", account.SecretAccessKey)
	assert.Equal(t, "http://localstack:4566", account.EndpointURL)

	cfg, err := registry.Resolve("")
	require.NoError(t, err)
	creds, err := cfg.Credentials.Retrieve(t.Context())
	require.NoError(t, err)
	assert.Equal(t, aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret", Source: "StaticCredentials"}, creds)
	require.NotNil(t, cfg.BaseEndpoint)
	assert.Equal(t, "http://localstack:4566", *cfg.BaseEndpoint)
	assert.Equal(t, "us-west-2", cfg.Region)
}

func TestResolve_UnknownAccount(t *testing.T) {
	registry := &Registry{
		accounts: map[string]Account{"dev": {Name: "dev", Region: "us-east-1", CredentialSource: CredentialSourceDefault}},
		fallback: "dev",
	}

	_, err := registry.Resolve("prod")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown account "prod"`)
}

func TestResolve_DefaultSource(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "default-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "default-secret")

	registry := &Registry{
		accounts: map[string]Account{
			"dev": {
				Name:             "dev",
				Region:           "eu-west-1",
				CredentialSource: CredentialSourceDefault,
			},
		},
		fallback: "dev",
	}

	cfg, err := registry.Resolve("")
	require.NoError(t, err)
	assert.Equal(t, "eu-west-1", cfg.Region)
	creds, err := cfg.Credentials.Retrieve(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "default-key", creds.AccessKeyID)
	assert.Equal(t, "default-secret", creds.SecretAccessKey)
}

func TestResolve_RoleSourceBuildsAssumeRoleConfig(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "default-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "default-secret")

	registry := &Registry{
		accounts: map[string]Account{
			"prod": {
				Name:             "prod",
				Region:           "us-east-1",
				CredentialSource: CredentialSourceRole,
				RoleARN:          "arn:aws:iam::123456789012:role/PraxisDriverRole",
				ExternalID:       "praxis-prod",
			},
		},
		fallback: "prod",
	}

	cfg, err := registry.Resolve("prod")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1", cfg.Region)
	require.NotNil(t, cfg.Credentials)
	assert.NotNil(t, cfg.Credentials)
}
