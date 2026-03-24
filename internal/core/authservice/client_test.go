package authservice

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildAWSConfig_SetsRegion(t *testing.T) {
	creds := CredentialResponse{AccessKeyID: "AKID", SecretAccessKey: "secret", Region: "eu-west-1"}
	cfg, err := buildAWSConfig(creds)
	require.NoError(t, err)
	assert.Equal(t, "eu-west-1", cfg.Region)
}

func TestBuildAWSConfig_SetsEndpoint(t *testing.T) {
	creds := CredentialResponse{AccessKeyID: "AKID", SecretAccessKey: "secret", Region: "us-east-1", EndpointURL: "http://localhost:4566"}
	cfg, err := buildAWSConfig(creds)
	require.NoError(t, err)
	require.NotNil(t, cfg.BaseEndpoint)
	assert.Equal(t, "http://localhost:4566", *cfg.BaseEndpoint)
}

func TestBuildResponse(t *testing.T) {
	cached := &CachedCredential{AccessKeyID: "key", SecretAccessKey: "secret", SessionToken: "token", ExpiresAt: "2025-01-01T00:00:00Z"}
	cfg := AccountConfig{Region: "us-east-1", EndpointURL: "http://localhost:4566"}
	resp := buildResponse(cached, cfg)
	assert.Equal(t, "key", resp.AccessKeyID)
	assert.Equal(t, "secret", resp.SecretAccessKey)
	assert.Equal(t, "token", resp.SessionToken)
	assert.Equal(t, "2025-01-01T00:00:00Z", resp.ExpiresAt)
	assert.Equal(t, "us-east-1", resp.Region)
	assert.Equal(t, "http://localhost:4566", resp.EndpointURL)
}

func TestIsCacheValid(t *testing.T) {
	assert.False(t, isCacheValid(nil))
	assert.True(t, isCacheValid(&CachedCredential{AccessKeyID: "key", SecretAccessKey: "secret"}))
	assert.True(t, isCacheValid(&CachedCredential{AccessKeyID: "key", SecretAccessKey: "secret", ExpiresAt: "2099-01-01T00:00:00Z"}))
	assert.False(t, isCacheValid(&CachedCredential{AccessKeyID: "key", SecretAccessKey: "secret", ExpiresAt: "2020-01-01T00:00:00Z"}))
	assert.False(t, isCacheValid(&CachedCredential{AccessKeyID: "key", SecretAccessKey: "secret", ExpiresAt: "not-a-date"}))
}
