package authservice

// Handler-level unit tests for the AuthService Restate Virtual Object.
//
// These tests drive the real handlers (GetCredentials, RefreshCredentials,
// GetStatus, Configure) against the Restate SDK's mock context
// (restate.WithMockContext + mocks.NewMockContext), so no Restate container
// is needed. STS calls are intercepted with a fake STSAPI injected via
// NewAuthServiceWithFactory. End-to-end flows through a real Restate runtime
// are covered in tests/integration/authservice_test.go.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test doubles and helpers
// ---------------------------------------------------------------------------

// fakeSTS is an STSAPI test double that counts calls and returns canned data.
type fakeSTS struct {
	assumeRoleCalls int
	lastRoleARN     string
	lastOpts        AssumeRoleOpts
	creds           *Credentials
	assumeErr       error

	identityCalls int
	identity      *CallerIdentity
	identityErr   error
}

func (f *fakeSTS) AssumeRole(_ context.Context, roleARN string, opts AssumeRoleOpts) (*Credentials, error) {
	f.assumeRoleCalls++
	f.lastRoleARN = roleARN
	f.lastOpts = opts
	if f.assumeErr != nil {
		return nil, f.assumeErr
	}
	return f.creds, nil
}

func (f *fakeSTS) GetCallerIdentity(_ context.Context) (*CallerIdentity, error) {
	f.identityCalls++
	if f.identityErr != nil {
		return nil, f.identityErr
	}
	return f.identity, nil
}

// fakeFactory returns an STS factory that always yields the given fake,
// ignoring the aws.Config it would normally build a client from.
func fakeFactory(f *fakeSTS) func(aws.Config) STSAPI {
	return func(aws.Config) STSAPI { return f }
}

// expectNoState makes Get("state") report no stored state (nil *AuthState).
func expectNoState(mockCtx *mocks.MockContext) {
	mockCtx.EXPECT().Get("state", mock.Anything).Return(false, nil)
}

// captureSetState records every Set("state", ...) call; *captured holds the
// most recent state written by the handler.
func captureSetState(mockCtx *mocks.MockContext, captured **AuthState) {
	mockCtx.On("Set", "state", mock.Anything).Run(func(args mock.Arguments) {
		*captured = args.Get(1).(*AuthState)
	})
}

// testNow is a fixed journaled clock value for deterministic cache branching.
var testNow = time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// GetStatus
// ---------------------------------------------------------------------------

// Regression test: GetStatus used to report an empty CredentialSource for
// bootstrap-configured accounts that had no Restate state yet (i.e. before the
// first GetCredentials call). It must now fall back to the bootstrap config.
func TestGetStatus_NilState_FallsBackToBootstrap(t *testing.T) {
	bootstrap := &AccountsConfig{Accounts: map[string]AccountConfig{
		"default": {
			Region:           "ca-central-1",
			CredentialSource: "static",
			AccessKeyID:      "AKID",
			SecretAccessKey:  "SECRET",
		},
	}}
	svc := NewAuthService(bootstrap)

	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("default")
	expectNoState(mockCtx)

	status, err := svc.GetStatus(restate.WithMockContext(mockCtx))
	require.NoError(t, err)

	assert.Equal(t, "default", status.AccountAlias)
	assert.Equal(t, "static", status.CredentialSource, "must report the bootstrap credential source")
	assert.Equal(t, "ca-central-1", status.Region, "must report the bootstrap region")
	assert.False(t, status.Valid, "no credential has been resolved yet")
	assert.Empty(t, status.ExpiresAt)
	assert.Empty(t, status.LastRefresh)
	assert.Empty(t, status.Error)
}

func TestGetStatus_NilState_UnknownAlias(t *testing.T) {
	bootstrap := &AccountsConfig{Accounts: map[string]AccountConfig{
		"default": {Region: "us-east-1", CredentialSource: "default"},
	}}
	svc := NewAuthService(bootstrap)

	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("ghost")
	expectNoState(mockCtx)

	status, err := svc.GetStatus(restate.WithMockContext(mockCtx))
	require.NoError(t, err)

	assert.Equal(t, "ghost", status.AccountAlias)
	assert.Empty(t, status.CredentialSource)
	assert.Empty(t, status.Region)
	assert.False(t, status.Valid)
}

func TestGetStatus_NilState_NilBootstrap(t *testing.T) {
	svc := NewAuthService(nil)

	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("default")
	expectNoState(mockCtx)

	status, err := svc.GetStatus(restate.WithMockContext(mockCtx))
	require.NoError(t, err)
	assert.Equal(t, "default", status.AccountAlias)
	assert.Empty(t, status.CredentialSource)
	assert.False(t, status.Valid)
}

func TestGetStatus_WithState_ValidCache(t *testing.T) {
	svc := NewAuthService(nil)
	st := &AuthState{
		Config: AccountConfig{Region: "eu-west-1", CredentialSource: "static"},
		CachedCredential: &CachedCredential{
			AccessKeyID:     "AKID",
			SecretAccessKey: "SECRET",
			// no ExpiresAt: static credentials never expire
		},
		LastRefresh: "2026-06-11T11:00:00Z",
	}

	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("prod")
	mockCtx.EXPECT().GetAndReturn("state", st)

	status, err := svc.GetStatus(restate.WithMockContext(mockCtx))
	require.NoError(t, err)

	assert.Equal(t, "prod", status.AccountAlias)
	assert.Equal(t, "static", status.CredentialSource)
	assert.Equal(t, "eu-west-1", status.Region)
	assert.True(t, status.Valid)
	assert.Equal(t, "2026-06-11T11:00:00Z", status.LastRefresh)
}

func TestGetStatus_WithState_ExpiredCache(t *testing.T) {
	svc := NewAuthService(nil)
	expired := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	st := &AuthState{
		Config:           AccountConfig{Region: "us-east-1", CredentialSource: "role", RoleARN: "arn:aws:iam::123456789012:role/x"},
		CachedCredential: &CachedCredential{AccessKeyID: "AKID", ExpiresAt: expired},
		Error:            "last refresh failed",
	}

	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("prod")
	mockCtx.EXPECT().GetAndReturn("state", st)

	status, err := svc.GetStatus(restate.WithMockContext(mockCtx))
	require.NoError(t, err)

	assert.False(t, status.Valid, "expired credential must not be reported valid")
	assert.Equal(t, expired, status.ExpiresAt)
	assert.Equal(t, "last refresh failed", status.Error)
}

// ---------------------------------------------------------------------------
// GetCredentials
// ---------------------------------------------------------------------------

func TestGetCredentials_StaticSource_BootstrapsAndCaches(t *testing.T) {
	bootstrap := &AccountsConfig{Accounts: map[string]AccountConfig{
		"default": {
			Region:           "us-east-1",
			CredentialSource: "static",
			AccessKeyID:      "AKID",
			SecretAccessKey:  "SECRET",
			EndpointURL:      "http://localhost:4566",
		},
	}}
	svc := NewAuthService(bootstrap)

	var captured *AuthState
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("default")
	expectNoState(mockCtx)
	mockCtx.EXPECT().RunAndReturn(testNow, nil) // journaledNow
	captureSetState(mockCtx, &captured)

	resp, err := svc.GetCredentials(restate.WithMockContext(mockCtx), "")
	require.NoError(t, err)

	assert.Equal(t, "AKID", resp.AccessKeyID)
	assert.Equal(t, "SECRET", resp.SecretAccessKey)
	assert.Empty(t, resp.SessionToken)
	assert.Empty(t, resp.ExpiresAt, "static credentials never expire")
	assert.Equal(t, "us-east-1", resp.Region)
	assert.Equal(t, "http://localhost:4566", resp.EndpointURL)

	require.NotNil(t, captured, "handler must persist state")
	require.NotNil(t, captured.CachedCredential)
	assert.Equal(t, "AKID", captured.CachedCredential.AccessKeyID)
	assert.Equal(t, testNow.Format(time.RFC3339), captured.LastRefresh)
	assert.Empty(t, captured.Error)
	assert.False(t, captured.RefreshScheduled, "static credentials need no refresh timer")
}

func TestGetCredentials_RoleSource_AssumesRoleThenServesFromCache(t *testing.T) {
	const roleARN = "arn:aws:iam::123456789012:role/praxis"
	expiry := testNow.Add(time.Hour)
	fake := &fakeSTS{creds: &Credentials{
		AccessKeyID:     "ASIATEMP",
		SecretAccessKey: "TEMPSECRET",
		SessionToken:    "TOKEN",
		ExpiresAt:       expiry,
	}}
	bootstrap := &AccountsConfig{Accounts: map[string]AccountConfig{
		"prod": {Region: "us-east-1", CredentialSource: "role", RoleARN: roleARN, ExternalID: "ext-1"},
	}}
	svc := NewAuthServiceWithFactory(bootstrap, fakeFactory(fake))

	// --- First call: cache miss, AssumeRole runs, refresh timer scheduled ---
	var captured *AuthState
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("prod")
	expectNoState(mockCtx)
	mockCtx.EXPECT().RunAndReturn(testNow, nil)             // journaledNow (typed *time.Time)
	mockCtx.EXPECT().RunAndExpect(mockCtx, fake.creds, nil) // AssumeRole Run block
	// Refresh is scheduled 10 minutes before the 1h expiry.
	mockCtx.EXPECT().MockObjectClient(ServiceName, "prod", "RefreshCredentials").
		MockSend("prod", restate.WithDelay(50*time.Minute))
	captureSetState(mockCtx, &captured)

	resp, err := svc.GetCredentials(restate.WithMockContext(mockCtx), "")
	require.NoError(t, err)

	assert.Equal(t, 1, fake.assumeRoleCalls, "first call must hit STS")
	assert.Equal(t, roleARN, fake.lastRoleARN)
	assert.Equal(t, "ext-1", fake.lastOpts.ExternalID)
	assert.Equal(t, time.Hour, fake.lastOpts.SessionDuration, "defaults to 1h when unset")

	assert.Equal(t, "ASIATEMP", resp.AccessKeyID)
	assert.Equal(t, "TOKEN", resp.SessionToken)
	assert.Equal(t, expiry.Format(time.RFC3339), resp.ExpiresAt)

	require.NotNil(t, captured)
	require.NotNil(t, captured.CachedCredential)
	assert.Equal(t, expiry.Format(time.RFC3339), captured.CachedCredential.ExpiresAt)
	assert.True(t, captured.RefreshScheduled)
	assert.Equal(t, testNow.Format(time.RFC3339), captured.LastRefresh)

	// --- Second call within validity: cache hit, no STS call ---
	mockCtx2 := mocks.NewMockContext(t)
	mockCtx2.EXPECT().Key().Return("prod")
	mockCtx2.EXPECT().GetAndReturn("state", captured)
	mockCtx2.EXPECT().RunAndReturn(testNow.Add(5*time.Minute), nil) // journaledNow

	resp2, err := svc.GetCredentials(restate.WithMockContext(mockCtx2), "")
	require.NoError(t, err)

	assert.Equal(t, 1, fake.assumeRoleCalls, "cache hit must not re-call STS")
	assert.Equal(t, resp, resp2, "cached response must match the original")
}

func TestGetCredentials_DefaultSource_ValidatesViaCallerIdentity(t *testing.T) {
	fake := &fakeSTS{identity: &CallerIdentity{Account: "123456789012", ARN: "arn:aws:iam::123456789012:user/dev"}}
	bootstrap := &AccountsConfig{Accounts: map[string]AccountConfig{
		"default": {Region: "us-east-1", CredentialSource: "default"},
	}}
	svc := NewAuthServiceWithFactory(bootstrap, fakeFactory(fake))

	var captured *AuthState
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("default")
	expectNoState(mockCtx)
	mockCtx.EXPECT().RunAndReturn(testNow, nil)                // journaledNow
	mockCtx.EXPECT().RunAndExpect(mockCtx, fake.identity, nil) // GetCallerIdentity Run block
	captureSetState(mockCtx, &captured)

	resp, err := svc.GetCredentials(restate.WithMockContext(mockCtx), "")
	require.NoError(t, err)

	assert.Equal(t, 1, fake.identityCalls)
	assert.Equal(t, SourceDefaultChain, resp.Source)
	assert.Empty(t, resp.AccessKeyID, "default-chain responses carry no inline keys")
	assert.Equal(t, "us-east-1", resp.Region)

	require.NotNil(t, captured)
	require.NotNil(t, captured.CachedCredential)
	assert.Equal(t, SourceDefaultChain, captured.CachedCredential.Source)
}

func TestGetCredentials_UnknownAlias_Terminal404(t *testing.T) {
	bootstrap := &AccountsConfig{Accounts: map[string]AccountConfig{
		"default": {Region: "us-east-1", CredentialSource: "default"},
	}}
	svc := NewAuthService(bootstrap)

	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("ghost")
	expectNoState(mockCtx)

	_, err := svc.GetCredentials(restate.WithMockContext(mockCtx), "")
	require.Error(t, err)
	assert.True(t, restate.IsTerminalError(err), "unknown alias must be terminal")
	assert.Equal(t, restate.Code(404), restate.ErrorCode(err))
}

func TestGetCredentials_StaticMissingKeys_Terminal401(t *testing.T) {
	bootstrap := &AccountsConfig{Accounts: map[string]AccountConfig{
		"default": {Region: "us-east-1", CredentialSource: "static"}, // no keys
	}}
	svc := NewAuthService(bootstrap)

	var captured *AuthState
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("default")
	expectNoState(mockCtx)
	mockCtx.EXPECT().RunAndReturn(testNow, nil)
	captureSetState(mockCtx, &captured)

	_, err := svc.GetCredentials(restate.WithMockContext(mockCtx), "")
	require.Error(t, err)
	assert.True(t, restate.IsTerminalError(err))
	assert.Equal(t, restate.Code(401), restate.ErrorCode(err))

	require.NotNil(t, captured, "failure must be persisted in state")
	assert.NotEmpty(t, captured.Error)
	assert.Nil(t, captured.CachedCredential)
}

func TestGetCredentials_UnsupportedSource_Terminal400(t *testing.T) {
	bootstrap := &AccountsConfig{Accounts: map[string]AccountConfig{
		"default": {Region: "us-east-1", CredentialSource: "magic"},
	}}
	svc := NewAuthService(bootstrap)

	var captured *AuthState
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("default")
	expectNoState(mockCtx)
	mockCtx.EXPECT().RunAndReturn(testNow, nil)
	captureSetState(mockCtx, &captured)

	_, err := svc.GetCredentials(restate.WithMockContext(mockCtx), "")
	require.Error(t, err)
	assert.True(t, restate.IsTerminalError(err))
	assert.Equal(t, restate.Code(400), restate.ErrorCode(err))
	require.NotNil(t, captured)
	assert.NotEmpty(t, captured.Error)
}

// ---------------------------------------------------------------------------
// RefreshCredentials
// ---------------------------------------------------------------------------

func TestRefreshCredentials_IgnoresValidCache(t *testing.T) {
	const roleARN = "arn:aws:iam::123456789012:role/praxis"
	newExpiry := testNow.Add(2 * time.Hour)
	fake := &fakeSTS{creds: &Credentials{
		AccessKeyID:     "ASIANEW",
		SecretAccessKey: "NEWSECRET",
		SessionToken:    "NEWTOKEN",
		ExpiresAt:       newExpiry,
	}}
	svc := NewAuthServiceWithFactory(nil, fakeFactory(fake))

	// Existing state with a still-valid cached credential and an armed timer.
	st := &AuthState{
		Config: AccountConfig{Region: "us-east-1", CredentialSource: "role", RoleARN: roleARN},
		CachedCredential: &CachedCredential{
			AccessKeyID: "ASIAOLD",
			ExpiresAt:   testNow.Add(time.Hour).Format(time.RFC3339),
		},
		RefreshScheduled: true,
	}

	var captured *AuthState
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("prod")
	mockCtx.EXPECT().GetAndReturn("state", st)
	mockCtx.EXPECT().RunAndReturn(testNow, nil)             // journaledNow
	mockCtx.EXPECT().RunAndExpect(mockCtx, fake.creds, nil) // AssumeRole Run block
	// RefreshScheduled is cleared first, so a new timer is armed for the new expiry.
	mockCtx.EXPECT().MockObjectClient(ServiceName, "prod", "RefreshCredentials").
		MockSend("prod", restate.WithDelay(110*time.Minute))
	captureSetState(mockCtx, &captured)

	resp, err := svc.RefreshCredentials(restate.WithMockContext(mockCtx), "")
	require.NoError(t, err)

	assert.Equal(t, 1, fake.assumeRoleCalls, "refresh must re-resolve even with a valid cache")
	assert.Equal(t, "ASIANEW", resp.AccessKeyID)
	assert.Equal(t, newExpiry.Format(time.RFC3339), resp.ExpiresAt)

	require.NotNil(t, captured)
	require.NotNil(t, captured.CachedCredential)
	assert.Equal(t, "ASIANEW", captured.CachedCredential.AccessKeyID)
	assert.True(t, captured.RefreshScheduled, "a new refresh timer must be armed")
	assert.Equal(t, testNow.Format(time.RFC3339), captured.LastRefresh)
}

// ---------------------------------------------------------------------------
// Configure
// ---------------------------------------------------------------------------

func TestConfigure_RegistersNewAlias(t *testing.T) {
	svc := NewAuthService(nil)
	cfg := AccountConfig{Region: "eu-west-1", CredentialSource: "role", RoleARN: "arn:aws:iam::123456789012:role/x"}

	var captured *AuthState
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("staging")
	expectNoState(mockCtx)
	captureSetState(mockCtx, &captured)

	err := svc.Configure(restate.WithMockContext(mockCtx), ConfigureRequest{Config: cfg})
	require.NoError(t, err)

	require.NotNil(t, captured)
	assert.Equal(t, cfg, captured.Config)
	assert.Nil(t, captured.CachedCredential)
	assert.False(t, captured.RefreshScheduled)
	assert.Empty(t, captured.Error)
}

func TestConfigure_ClearsCachedCredentialAndError(t *testing.T) {
	svc := NewAuthService(nil)
	st := &AuthState{
		Config:           AccountConfig{Region: "us-east-1", CredentialSource: "static", AccessKeyID: "OLD", SecretAccessKey: "OLD"},
		CachedCredential: &CachedCredential{AccessKeyID: "OLD", SecretAccessKey: "OLD"},
		RefreshScheduled: true,
		Error:            "previous failure",
	}
	newCfg := AccountConfig{Region: "us-west-2", CredentialSource: "static", AccessKeyID: "NEW", SecretAccessKey: "NEW"}

	var captured *AuthState
	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("default")
	mockCtx.EXPECT().GetAndReturn("state", st)
	captureSetState(mockCtx, &captured)

	err := svc.Configure(restate.WithMockContext(mockCtx), ConfigureRequest{Config: newCfg})
	require.NoError(t, err)

	require.NotNil(t, captured)
	assert.Equal(t, newCfg, captured.Config)
	assert.Nil(t, captured.CachedCredential, "config change must invalidate the cache")
	assert.False(t, captured.RefreshScheduled)
	assert.Empty(t, captured.Error)
}

func TestConfigure_InvalidConfig_Terminal400(t *testing.T) {
	svc := NewAuthService(nil)
	// static source without keys fails validation before any state access
	cfg := AccountConfig{Region: "us-east-1", CredentialSource: "static"}

	mockCtx := mocks.NewMockContext(t)
	mockCtx.EXPECT().Key().Return("default")

	err := svc.Configure(restate.WithMockContext(mockCtx), ConfigureRequest{Config: cfg})
	require.Error(t, err)
	assert.True(t, restate.IsTerminalError(err))
	assert.Equal(t, restate.Code(400), restate.ErrorCode(err))
}

// ---------------------------------------------------------------------------
// classifySTSError
// ---------------------------------------------------------------------------

func TestClassifySTSError(t *testing.T) {
	terminalCases := []struct {
		name string
		code string
		want restate.Code
	}{
		{"access denied", "AccessDenied", 403},
		{"access denied exception", "AccessDeniedException", 403},
		{"unauthorized", "UnauthorizedAccess", 403},
		{"validation error", "ValidationError", 400},
		{"invalid parameter", "InvalidParameterValue", 400},
		{"malformed policy", "MalformedPolicyDocument", 400},
		{"region disabled", "RegionDisabledException", 400},
		{"invalid identity token", "InvalidIdentityToken", 400},
	}
	for _, tc := range terminalCases {
		t.Run(tc.name, func(t *testing.T) {
			cause := &smithy.GenericAPIError{Code: tc.code, Message: "nope"}
			authErr := errAssumeRole("prod", cause)

			err := classifySTSError(authErr, cause)
			require.Error(t, err)
			assert.True(t, restate.IsTerminalError(err), "%s must be terminal", tc.code)
			assert.Equal(t, tc.want, restate.ErrorCode(err))

			gotAuthErr, ok := AsAuthError(err)
			require.True(t, ok, "AuthError must survive the TerminalError wrap")
			assert.Equal(t, ErrCodeAssumeRole, gotAuthErr.Code)
		})
	}

	retryableCases := []struct {
		name  string
		cause error
	}{
		{"throttling", &smithy.GenericAPIError{Code: "Throttling", Message: "slow down"}},
		{"request limit", &smithy.GenericAPIError{Code: "RequestLimitExceeded", Message: "limit"}},
		{"network error", errors.New("dial tcp 1.2.3.4:443: connection refused")},
	}
	for _, tc := range retryableCases {
		t.Run(tc.name, func(t *testing.T) {
			authErr := errAssumeRole("prod", tc.cause)

			err := classifySTSError(authErr, tc.cause)
			require.Error(t, err)
			assert.False(t, restate.IsTerminalError(err), "%s must stay retryable for Restate", tc.name)
			assert.Same(t, authErr, err, "retryable errors are returned unwrapped")
		})
	}
}

// ---------------------------------------------------------------------------
// isCacheValidAt boundaries
// ---------------------------------------------------------------------------

func TestIsCacheValidAt_Boundaries(t *testing.T) {
	now := testNow

	assert.False(t, isCacheValidAt(nil, now), "nil credential is never valid")
	assert.True(t, isCacheValidAt(&CachedCredential{AccessKeyID: "k"}, now),
		"credentials without expiry never go stale")
	assert.True(t, isCacheValidAt(&CachedCredential{
		ExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339),
	}, now), "exactly 5 minutes remaining is still valid (>=)")
	assert.False(t, isCacheValidAt(&CachedCredential{
		ExpiresAt: now.Add(5*time.Minute - time.Second).Format(time.RFC3339),
	}, now), "less than 5 minutes remaining is invalid")
	assert.False(t, isCacheValidAt(&CachedCredential{
		ExpiresAt: now.Add(-time.Hour).Format(time.RFC3339),
	}, now), "expired credential is invalid")
	assert.False(t, isCacheValidAt(&CachedCredential{ExpiresAt: "not-a-timestamp"}, now),
		"unparseable expiry is treated as invalid")
}
