// Package authservice implements the centralized AWS credential lifecycle
// manager for Praxis. It runs as a Restate Virtual Object keyed by account
// alias (e.g., "default", "prod-us", "staging"), providing:
//
//   - Credential resolution: static keys, STS AssumeRole, or default chain
//   - Credential caching: avoids redundant STS calls across drivers
//   - Proactive refresh: schedules a durable timer to refresh role credentials
//     before they expire, so drivers never see expired tokens
//   - Runtime configuration: accounts can be registered/updated via the
//     Configure handler without restarting the service
//
// Drivers and Core components access credentials through the AuthClient
// interface (see client.go) which calls this service via Restate RPC.
package authservice

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	restate "github.com/restatedev/sdk-go"
)

// ServiceName is the Restate Virtual Object name for the Auth Service.
const ServiceName = "AuthService"

// AuthService is a Restate Virtual Object that manages AWS credential lifecycle.
// Each instance is keyed by an account-alias (e.g., "prod-us", "staging", "default").
//
// The stsFactory is injected to enable testing with mock STS implementations.
// In production, it creates a real STS client; in tests, it returns a mock
// that avoids actual AWS API calls.
type AuthService struct {
	bootstrap  *AccountsConfig         // env-var seed for first-boot; nil in production
	stsFactory func(aws.Config) STSAPI // creates STS API wrapper; mockable for tests
}

// NewAuthService creates an AuthService with the default STS factory.
// bootstrap is the env-var seed for first-boot; pass nil in production.
func NewAuthService(bootstrap *AccountsConfig) *AuthService {
	return &AuthService{
		bootstrap:  bootstrap,
		stsFactory: NewSTSAPI,
	}
}

// NewAuthServiceWithFactory creates an AuthService with a custom STS factory (for tests).
func NewAuthServiceWithFactory(bootstrap *AccountsConfig, factory func(aws.Config) STSAPI) *AuthService {
	return &AuthService{
		bootstrap:  bootstrap,
		stsFactory: factory,
	}
}

// ServiceName returns the Restate Virtual Object name.
func (a *AuthService) ServiceName() string {
	return ServiceName
}

// GetCredentials returns cached or fresh AWS credentials for the account-alias key.
// This is the primary entry point called by drivers and Core components.
//
// Flow:
//  1. Resolve account config (Restate state → bootstrap env fallback)
//  2. Check credential cache validity (not expired, >5min remaining)
//  3. If cache hit: return immediately (fast path)
//  4. If cache miss: resolve fresh credentials via the configured source
//  5. Cache the result and return
//
// The unused string parameter exists because Restate handler signatures require
// an input type; the actual account alias comes from restate.Key(ctx).
func (a *AuthService) GetCredentials(ctx restate.ObjectContext, _ string) (CredentialResponse, error) {
	alias := restate.Key(ctx)

	cfg, err := a.resolveConfig(ctx, alias)
	if err != nil {
		return CredentialResponse{}, err
	}

	state, err := restate.Get[*AuthState](ctx, "state")
	if err != nil {
		return CredentialResponse{}, err
	}
	if state == nil {
		state = &AuthState{Config: cfg}
	}

	// Check cache validity for non-force requests
	if isCacheValid(state.CachedCredential) {
		return buildResponse(state.CachedCredential, cfg), nil
	}

	// Resolve fresh credentials
	if err := a.resolveCredentials(ctx, state, cfg, alias); err != nil {
		state.Error = err.Error()
		restate.Set(ctx, "state", state)
		return CredentialResponse{}, err
	}

	state.LastRefresh = time.Now().UTC().Format(time.RFC3339)
	state.Error = ""
	restate.Set(ctx, "state", state)

	return buildResponse(state.CachedCredential, cfg), nil
}

// RefreshCredentials force-refreshes credentials, ignoring the cache.
func (a *AuthService) RefreshCredentials(ctx restate.ObjectContext, _ string) (CredentialResponse, error) {
	alias := restate.Key(ctx)

	cfg, err := a.resolveConfig(ctx, alias)
	if err != nil {
		return CredentialResponse{}, err
	}

	state, err := restate.Get[*AuthState](ctx, "state")
	if err != nil {
		return CredentialResponse{}, err
	}
	if state == nil {
		state = &AuthState{Config: cfg}
	}

	// Clear refresh flag — we're doing the refresh now
	state.RefreshScheduled = false

	// Always resolve fresh (ignore cache)
	state.CachedCredential = nil
	if err := a.resolveCredentials(ctx, state, cfg, alias); err != nil {
		state.Error = err.Error()
		restate.Set(ctx, "state", state)
		return CredentialResponse{}, err
	}

	state.LastRefresh = time.Now().UTC().Format(time.RFC3339)
	state.Error = ""
	restate.Set(ctx, "state", state)

	return buildResponse(state.CachedCredential, cfg), nil
}

// GetStatus returns credential health without triggering a refresh (shared handler).
func (a *AuthService) GetStatus(ctx restate.ObjectSharedContext) (CredentialStatus, error) {
	alias := restate.Key(ctx)

	state, err := restate.Get[*AuthState](ctx, "state")
	if err != nil {
		return CredentialStatus{}, err
	}
	if state == nil {
		return CredentialStatus{
			AccountAlias: alias,
			Valid:        false,
		}, nil
	}

	status := CredentialStatus{
		AccountAlias:     alias,
		CredentialSource: state.Config.CredentialSource,
		Region:           state.Config.Region,
		Valid:            isCacheValid(state.CachedCredential),
		LastRefresh:      state.LastRefresh,
		Error:            state.Error,
	}
	if state.CachedCredential != nil {
		status.ExpiresAt = state.CachedCredential.ExpiresAt
	}
	return status, nil
}

// Configure updates the account configuration at runtime.
func (a *AuthService) Configure(ctx restate.ObjectContext, req ConfigureRequest) error {
	alias := restate.Key(ctx)

	if err := req.Config.Validate(alias); err != nil {
		return restate.TerminalError(fmt.Errorf("invalid config for %q: %w", alias, err), 400)
	}

	state, err := restate.Get[*AuthState](ctx, "state")
	if err != nil {
		return err
	}
	if state == nil {
		state = &AuthState{}
	}

	state.Config = req.Config
	state.CachedCredential = nil // clear cache on config change
	state.RefreshScheduled = false
	state.Error = ""
	restate.Set(ctx, "state", state)
	return nil
}

// resolveConfig checks Restate state first, then bootstrap env fallback.
func (a *AuthService) resolveConfig(ctx restate.ObjectContext, alias string) (AccountConfig, error) {
	state, err := restate.Get[*AuthState](ctx, "state")
	if err != nil {
		return AccountConfig{}, err
	}
	if state != nil && state.Config.Region != "" {
		return state.Config, nil
	}

	if a.bootstrap != nil {
		if cfg, ok := a.bootstrap.Accounts[alias]; ok {
			restate.Set(ctx, "state", &AuthState{Config: cfg})
			return cfg, nil
		}
	}

	return AccountConfig{}, restate.TerminalError(
		fmt.Errorf("account %q is not configured — call Configure to register it", alias), 404,
	)
}

// resolveCredentials resolves fresh credentials based on the credential source.
// Dispatches to source-specific resolvers:
//   - "static": uses inline access key ID + secret from config
//   - "role": calls STS AssumeRole and caches temporary credentials
//   - "" or "default": uses the AWS default credential chain (env, IMDS, etc.)
func (a *AuthService) resolveCredentials(ctx restate.ObjectContext, state *AuthState, cfg AccountConfig, alias string) error {
	switch cfg.CredentialSource {
	case "static":
		return a.resolveStatic(state, cfg, alias)
	case "role":
		return a.resolveRole(ctx, state, cfg, alias)
	case "", "default":
		return a.resolveDefault(ctx, state, cfg, alias)
	default:
		return restate.TerminalError(errUnsupportedSource(alias, cfg.CredentialSource), 400)
	}
}

// resolveStatic validates and stores static (long-lived) AWS credentials.
// Static credentials never expire, so no refresh timer is scheduled.
func (a *AuthService) resolveStatic(state *AuthState, cfg AccountConfig, alias string) error {
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return restate.TerminalError(errMissingStaticCredentials(alias), 401)
	}
	state.CachedCredential = &CachedCredential{
		AccessKeyID:     cfg.AccessKeyID,
		SecretAccessKey: cfg.SecretAccessKey,
	}
	return nil
}

// resolveRole calls STS AssumeRole to obtain temporary credentials for a
// cross-account or role-based configuration. On success, caches the credentials
// and schedules a proactive refresh 10 minutes before expiry.
func (a *AuthService) resolveRole(ctx restate.ObjectContext, state *AuthState, cfg AccountConfig, alias string) error {
	if cfg.RoleARN == "" {
		return restate.TerminalError(errMissingRoleARN(alias), 401)
	}

	baseCfg, err := resolveBaseConfig(cfg)
	if err != nil {
		authErr := errConfigLoad(alias, err)
		if authErr.IsRetryable() {
			return authErr
		}
		return restate.TerminalError(authErr, restate.Code(authErr.HTTPCode()))
	}

	stsAPI := a.stsFactory(baseCfg)
	duration := cfg.SessionDuration
	if duration == 0 {
		duration = time.Hour
	}

	creds, err := restate.Run(ctx, func(rc restate.RunContext) (*Credentials, error) {
		return stsAPI.AssumeRole(rc, cfg.RoleARN, AssumeRoleOpts{
			ExternalID:      cfg.ExternalID,
			SessionDuration: duration,
		})
	})
	if err != nil {
		authErr := errAssumeRole(alias, err)
		if isAccessDenied(err) {
			return restate.TerminalError(authErr, 403)
		}
		if authErr.IsRetryable() {
			return authErr
		}
		return restate.TerminalError(authErr, restate.Code(authErr.HTTPCode()))
	}

	state.CachedCredential = &CachedCredential{
		AccessKeyID:     creds.AccessKeyID,
		SecretAccessKey: creds.SecretAccessKey,
		SessionToken:    creds.SessionToken,
		ExpiresAt:       creds.ExpiresAt.UTC().Format(time.RFC3339),
	}

	a.scheduleRefresh(ctx, state, creds.ExpiresAt)
	return nil
}

func (a *AuthService) resolveDefault(ctx restate.ObjectContext, state *AuthState, cfg AccountConfig, alias string) error {
	baseCfg, err := resolveBaseConfig(cfg)
	if err != nil {
		authErr := errConfigLoad(alias, err)
		if authErr.IsRetryable() {
			return authErr
		}
		return restate.TerminalError(authErr, restate.Code(authErr.HTTPCode()))
	}

	stsAPI := a.stsFactory(baseCfg)

	// Validate credentials work via GetCallerIdentity
	_, err = restate.Run(ctx, func(rc restate.RunContext) (*CallerIdentity, error) {
		return stsAPI.GetCallerIdentity(rc)
	})
	if err != nil {
		authErr := errCredentialRetrieval(alias, err)
		if authErr.IsRetryable() {
			return authErr
		}
		return restate.TerminalError(authErr, restate.Code(authErr.HTTPCode()))
	}

	// Default chain manages its own refresh — we just store the fact that it works
	state.CachedCredential = &CachedCredential{
		AccessKeyID:     "default-chain",
		SecretAccessKey: "default-chain",
	}
	return nil
}

// scheduleRefresh schedules a proactive credential refresh before expiry.
// Uses a Restate delayed message (durable timer) so the refresh survives
// process restarts. The delay is set to 10 minutes before expiry, with a
// minimum of 1 minute to avoid scheduling in the past.
// The RefreshScheduled flag prevents duplicate timers if scheduleRefresh
// is called multiple times for the same credential.
func (a *AuthService) scheduleRefresh(ctx restate.ObjectContext, state *AuthState, expiresAt time.Time) {
	if state.RefreshScheduled {
		return
	}

	delay := time.Until(expiresAt) - 10*time.Minute
	delay = max(delay, time.Minute)

	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "RefreshCredentials").
		Send(restate.Key(ctx), restate.WithDelay(delay))
	state.RefreshScheduled = true
}

// isCacheValid checks whether cached credentials are still usable.
// Credentials without an expiry (static/default) are always valid.
// Temporary credentials (from AssumeRole) are valid if they have at
// least 5 minutes of remaining lifetime, providing a safety margin
// for in-flight requests.
func isCacheValid(cached *CachedCredential) bool {
	if cached == nil {
		return false
	}
	if cached.ExpiresAt == "" {
		return true
	}
	expiry, err := time.Parse(time.RFC3339, cached.ExpiresAt)
	if err != nil {
		return false
	}
	return time.Until(expiry) >= 5*time.Minute
}

// buildResponse maps cached credentials and account config into a
// CredentialResponse suitable for JSON serialization over Restate RPC.
func buildResponse(cached *CachedCredential, cfg AccountConfig) CredentialResponse {
	resp := CredentialResponse{
		Region:      cfg.Region,
		EndpointURL: cfg.EndpointURL,
	}
	if cached != nil {
		resp.AccessKeyID = cached.AccessKeyID
		resp.SecretAccessKey = cached.SecretAccessKey
		resp.SessionToken = cached.SessionToken
		resp.ExpiresAt = cached.ExpiresAt
	}
	return resp
}

// resolveBaseConfig creates an aws.Config from account settings. For static
// credentials, it injects a static credentials provider. For other sources,
// it uses the default credential chain. If EndpointURL is set (Moto),
// it configures the BaseEndpoint accordingly.
func resolveBaseConfig(account AccountConfig) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(account.Region),
	}
	if account.CredentialSource == "static" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				account.AccessKeyID, account.SecretAccessKey, "",
			),
		))
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("load base AWS config: %w", err)
	}
	if account.EndpointURL != "" {
		cfg.BaseEndpoint = aws.String(account.EndpointURL)
	}
	return cfg, nil
}
