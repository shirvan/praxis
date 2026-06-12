//go:build integration

// AuthService end-to-end tests: invoke the Virtual Object handlers through a
// real Restate container (via restatetest) exactly as drivers and the CLI do,
// with Moto serving the AWS APIs (STS AssumeRole).
//
// Run with: go test ./tests/integration/ -run 'TestAuthService' -tags=integration -timeout=5m
package integration

import (
	"testing"
	"time"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/restatetest"
)

// setupAuthService boots a Restate test environment with only the AuthService
// bound, bootstrapped from the standard PRAXIS_ACCOUNT_* env vars (static
// "local" account pointing at Moto).
func setupAuthService(t *testing.T) *ingress.Client {
	t.Helper()
	configureLocalAccount(t)

	env := restatetest.Start(t,
		restate.Reflect(authservice.NewAuthService(authservice.LoadBootstrapFromEnv())),
	)
	return env.Ingress()
}

func getStatus(t *testing.T, client *ingress.Client, alias string) authservice.CredentialStatus {
	t.Helper()
	status, err := ingress.Object[restate.Void, authservice.CredentialStatus](
		client, authservice.ServiceName, alias, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err, "GetStatus(%s) should not fail", alias)
	return status
}

func getCredentials(t *testing.T, client *ingress.Client, alias string) authservice.CredentialResponse {
	t.Helper()
	creds, err := ingress.Object[string, authservice.CredentialResponse](
		client, authservice.ServiceName, alias, "GetCredentials",
	).Request(t.Context(), "")
	require.NoError(t, err, "GetCredentials(%s) should not fail", alias)
	return creds
}

// Regression test: GetStatus on a bootstrap-configured alias must report the
// bootstrap credential source and region even before the first GetCredentials
// call creates Restate state. It previously returned an empty credentialSource.
func TestAuthService_GetStatusBeforeFirstResolution(t *testing.T) {
	client := setupAuthService(t)

	status := getStatus(t, client, integrationAccountName)

	assert.Equal(t, integrationAccountName, status.AccountAlias)
	assert.Equal(t, "static", status.CredentialSource,
		"bootstrap-configured account must report its credential source before first resolution")
	assert.Equal(t, "us-east-1", status.Region)
	assert.False(t, status.Valid, "no credential resolved yet")
	assert.Empty(t, status.LastRefresh)
}

func TestAuthService_GetStatusUnknownAlias(t *testing.T) {
	client := setupAuthService(t)

	status := getStatus(t, client, "no-such-account")

	assert.Equal(t, "no-such-account", status.AccountAlias)
	assert.Empty(t, status.CredentialSource)
	assert.Empty(t, status.Region)
	assert.False(t, status.Valid)
}

func TestAuthService_GetCredentialsStaticSource(t *testing.T) {
	client := setupAuthService(t)

	creds := getCredentials(t, client, integrationAccountName)
	assert.Equal(t, "test", creds.AccessKeyID)
	assert.Equal(t, "test", creds.SecretAccessKey)
	assert.Empty(t, creds.SessionToken)
	assert.Empty(t, creds.ExpiresAt, "static credentials never expire")
	assert.Equal(t, "us-east-1", creds.Region)
	assert.Equal(t, motoEndpoint, creds.EndpointURL)

	// Resolution must be reflected in status.
	status := getStatus(t, client, integrationAccountName)
	assert.True(t, status.Valid, "cached static credential is valid")
	assert.Equal(t, "static", status.CredentialSource)
	assert.NotEmpty(t, status.LastRefresh)
	assert.Empty(t, status.Error)
}

func TestAuthService_ConfigureThenGetStatus(t *testing.T) {
	client := setupAuthService(t)
	const alias = "secondary"

	// Before Configure: unknown.
	before := getStatus(t, client, alias)
	assert.Empty(t, before.CredentialSource)

	req := authservice.ConfigureRequest{Config: authservice.AccountConfig{
		Region:           "eu-west-1",
		CredentialSource: "static",
		AccessKeyID:      "secondary-key",
		SecretAccessKey:  "secondary-secret",
		EndpointURL:      motoEndpoint,
	}}
	_, err := ingress.Object[authservice.ConfigureRequest, restate.Void](
		client, authservice.ServiceName, alias, "Configure",
	).Request(t.Context(), req)
	require.NoError(t, err, "Configure(%s) should succeed", alias)

	after := getStatus(t, client, alias)
	assert.Equal(t, "static", after.CredentialSource, "GetStatus must reflect the configured account")
	assert.Equal(t, "eu-west-1", after.Region)
	assert.False(t, after.Valid, "configuring does not resolve credentials")

	creds := getCredentials(t, client, alias)
	assert.Equal(t, "secondary-key", creds.AccessKeyID)
	assert.Equal(t, "eu-west-1", creds.Region)
}

// TestAuthService_RoleSourceViaMoto exercises the role credential source
// against Moto's STS AssumeRole, then verifies RefreshCredentials rotates the
// session.
func TestAuthService_RoleSourceViaMoto(t *testing.T) {
	client := setupAuthService(t)
	const alias = "role-acct"

	// The base STS call uses the default credential chain; Moto accepts any keys.
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

	req := authservice.ConfigureRequest{Config: authservice.AccountConfig{
		Region:           "us-east-1",
		CredentialSource: "role",
		RoleARN:          "arn:aws:iam::123456789012:role/praxis-integration",
		EndpointURL:      motoEndpoint,
	}}
	_, err := ingress.Object[authservice.ConfigureRequest, restate.Void](
		client, authservice.ServiceName, alias, "Configure",
	).Request(t.Context(), req)
	require.NoError(t, err)

	creds := getCredentials(t, client, alias)
	assert.NotEmpty(t, creds.AccessKeyID, "AssumeRole must return temporary keys")
	assert.NotEmpty(t, creds.SessionToken, "temporary credentials carry a session token")
	require.NotEmpty(t, creds.ExpiresAt)
	expiry, err := time.Parse(time.RFC3339, creds.ExpiresAt)
	require.NoError(t, err)
	assert.True(t, expiry.After(time.Now().UTC()), "expiry must be in the future")

	status := getStatus(t, client, alias)
	assert.True(t, status.Valid)
	assert.Equal(t, "role", status.CredentialSource)
	assert.Equal(t, creds.ExpiresAt, status.ExpiresAt)

	// A second GetCredentials within validity is served from cache.
	cached := getCredentials(t, client, alias)
	assert.Equal(t, creds, cached, "second call within validity must be the cached credential")

	// Force refresh ignores the cache and mints a new session.
	refreshed, err := ingress.Object[string, authservice.CredentialResponse](
		client, authservice.ServiceName, alias, "RefreshCredentials",
	).Request(t.Context(), "")
	require.NoError(t, err)
	assert.NotEmpty(t, refreshed.SessionToken)
	assert.NotEqual(t, creds.SessionToken, refreshed.SessionToken,
		"refresh must mint a new STS session")
}
