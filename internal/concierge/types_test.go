package concierge

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConciergeConfigurationDefaults(t *testing.T) {
	cfg := ConciergeConfiguration{Provider: "openai", Model: "gpt-4o"}
	got := cfg.Defaults()

	assert.Equal(t, 20, got.MaxTurns)
	assert.Equal(t, 200, got.MaxMessages)
	assert.InDelta(t, 0.1, got.Temperature, 0.001)
	assert.Equal(t, "24h", got.SessionTTL)
	assert.Equal(t, "5m", got.ApprovalTTL)
}

func TestConciergeConfigurationDefaultsPreservesExisting(t *testing.T) {
	cfg := ConciergeConfiguration{
		Provider:    "claude",
		Model:       "claude-sonnet-4-20250514",
		MaxTurns:    10,
		MaxMessages: 50,
		Temperature: 0.5,
		SessionTTL:  "1h",
		ApprovalTTL: "10m",
	}
	got := cfg.Defaults()

	assert.Equal(t, 10, got.MaxTurns)
	assert.Equal(t, 50, got.MaxMessages)
	assert.InDelta(t, 0.5, got.Temperature, 0.001)
	assert.Equal(t, "1h", got.SessionTTL)
	assert.Equal(t, "10m", got.ApprovalTTL)
}

func TestIsConfigured(t *testing.T) {
	assert.False(t, ConciergeConfiguration{}.IsConfigured())
	assert.False(t, ConciergeConfiguration{Provider: "openai"}.IsConfigured())
	assert.False(t, ConciergeConfiguration{Model: "gpt-4"}.IsConfigured())
	assert.True(t, ConciergeConfiguration{Provider: "openai", Model: "gpt-4"}.IsConfigured())
}

func TestRedacted(t *testing.T) {
	cfg := ConciergeConfiguration{Provider: "openai", Model: "gpt-4", APIKey: "sk-secret123"}
	got := cfg.Redacted()

	assert.Equal(t, "***", got.APIKey)
	assert.Equal(t, "openai", got.Provider)
}

func TestRedactedNoKey(t *testing.T) {
	cfg := ConciergeConfiguration{Provider: "openai", Model: "gpt-4"}
	got := cfg.Redacted()

	assert.Equal(t, "", got.APIKey)
}

func TestInitSession(t *testing.T) {
	req := AskRequest{Prompt: "hello", Account: "prod", Workspace: "staging"}
	state := initSession(req, "1h", "2025-01-01T00:00:00Z")

	assert.Equal(t, "prod", state.Account)
	assert.Equal(t, "staging", state.Workspace)
	assert.Equal(t, "2025-01-01T00:00:00Z", state.CreatedAt)
	assert.NotEmpty(t, state.ExpiresAt)
}
