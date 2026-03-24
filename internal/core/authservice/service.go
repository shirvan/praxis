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
type AuthService struct {
	bootstrap  *AccountsConfig
	stsFactory func(aws.Config) STSAPI
}

// NewAuthService creates an AuthService with the default STS factory.
// bootstrap is the env-var seed for first-boot; pass nil in production.
func NewAuthService(bootstrap *AccountsConfig) *AuthService {
	return &AuthService{
		bootstrap:  bootstrap,
		stsFactory: func(cfg aws.Config) STSAPI { return NewSTSAPI(cfg) },
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
func (a *AuthService) scheduleRefresh(ctx restate.ObjectContext, state *AuthState, expiresAt time.Time) {
	if state.RefreshScheduled {
		return
	}

	delay := time.Until(expiresAt) - 10*time.Minute
	if delay < time.Minute {
		delay = time.Minute
	}

	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "RefreshCredentials").
		Send(restate.Key(ctx), restate.WithDelay(delay))
	state.RefreshScheduled = true
}

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
