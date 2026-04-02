package concierge

import (
	"fmt"

	restate "github.com/restatedev/sdk-go"
)

// ConciergeConfig is a Restate Virtual Object keyed by "global".
type ConciergeConfig struct{}

func (ConciergeConfig) ServiceName() string { return ConciergeConfigServiceName }

// Configure sets or updates the LLM provider configuration.
func (ConciergeConfig) Configure(ctx restate.ObjectContext, req ConciergeConfigRequest) error {
	if req.Provider == "" {
		return restate.TerminalError(fmt.Errorf("provider is required"), 400)
	}
	if req.Model == "" {
		return restate.TerminalError(fmt.Errorf("model is required"), 400)
	}
	if req.Provider != "openai" && req.Provider != "claude" {
		return restate.TerminalError(fmt.Errorf("provider must be 'openai' or 'claude', got %q", req.Provider), 400)
	}

	// Load existing config to preserve fields not in this request.
	existing, err := restate.Get[*ConciergeConfiguration](ctx, "config")
	if err != nil {
		return err
	}

	cfg := ConciergeConfiguration{
		Provider: req.Provider,
		Model:    req.Model,
	}

	// Apply request values, falling back to existing config, then defaults.
	cfg.APIKey = req.APIKey
	cfg.APIKeyRef = req.APIKeyRef
	cfg.BaseURL = req.BaseURL

	cfg.MaxTurns = req.MaxTurns
	if cfg.MaxTurns == 0 && existing != nil {
		cfg.MaxTurns = existing.MaxTurns
	}
	cfg.MaxMessages = req.MaxMessages
	if cfg.MaxMessages == 0 && existing != nil {
		cfg.MaxMessages = existing.MaxMessages
	}
	cfg.Temperature = req.Temperature
	if cfg.Temperature == 0 && existing != nil {
		cfg.Temperature = existing.Temperature
	}
	cfg.SessionTTL = req.SessionTTL
	if cfg.SessionTTL == "" && existing != nil {
		cfg.SessionTTL = existing.SessionTTL
	}
	cfg.ApprovalTTL = req.ApprovalTTL
	if cfg.ApprovalTTL == "" && existing != nil {
		cfg.ApprovalTTL = existing.ApprovalTTL
	}

	// Apply defaults for any remaining zero values.
	cfg = cfg.Defaults()

	restate.Set(ctx, "config", cfg)
	return nil
}

// Get returns the current configuration with secrets redacted.
func (ConciergeConfig) Get(ctx restate.ObjectSharedContext) (ConciergeConfiguration, error) {
	cfgPtr, err := restate.Get[*ConciergeConfiguration](ctx, "config")
	if err != nil {
		return ConciergeConfiguration{}, err
	}
	if cfgPtr == nil {
		return ConciergeConfiguration{}, nil
	}
	return cfgPtr.Redacted(), nil
}

// GetFull returns the unredacted configuration (internal service-to-service only).
func (ConciergeConfig) GetFull(ctx restate.ObjectSharedContext) (*ConciergeConfiguration, error) {
	return restate.Get[*ConciergeConfiguration](ctx, "config")
}
