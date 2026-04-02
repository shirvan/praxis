package concierge

import (
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigureThenGet(t *testing.T) {
	env := restatetest.Start(t, restate.Reflect(ConciergeConfig{}))
	client := env.Ingress()

	_, err := ingress.Object[ConciergeConfigRequest, restate.Void](
		client, ConciergeConfigServiceName, "global", "Configure",
	).Request(t.Context(), ConciergeConfigRequest{
		Provider: "openai",
		Model:    "gpt-4o",
		APIKey:   "sk-test123",
	})
	require.NoError(t, err)

	cfg, err := ingress.Object[restate.Void, ConciergeConfiguration](
		client, ConciergeConfigServiceName, "global", "Get",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	assert.Equal(t, "openai", cfg.Provider)
	assert.Equal(t, "gpt-4o", cfg.Model)
	assert.Equal(t, "***", cfg.APIKey, "API key should be redacted")
	assert.Equal(t, 20, cfg.MaxTurns, "should apply defaults")
	assert.Equal(t, 200, cfg.MaxMessages)
}

func TestConfigureInvalidProvider(t *testing.T) {
	env := restatetest.Start(t, restate.Reflect(ConciergeConfig{}))
	client := env.Ingress()

	_, err := ingress.Object[ConciergeConfigRequest, restate.Void](
		client, ConciergeConfigServiceName, "global", "Configure",
	).Request(t.Context(), ConciergeConfigRequest{
		Provider: "gemini",
		Model:    "gemini-pro",
	})
	assert.Error(t, err)
}

func TestConfigureMissingProvider(t *testing.T) {
	env := restatetest.Start(t, restate.Reflect(ConciergeConfig{}))
	client := env.Ingress()

	_, err := ingress.Object[ConciergeConfigRequest, restate.Void](
		client, ConciergeConfigServiceName, "global", "Configure",
	).Request(t.Context(), ConciergeConfigRequest{
		Model: "gpt-4o",
	})
	assert.Error(t, err)
}

func TestGetUnconfigured(t *testing.T) {
	env := restatetest.Start(t, restate.Reflect(ConciergeConfig{}))
	client := env.Ingress()

	cfg, err := ingress.Object[restate.Void, ConciergeConfiguration](
		client, ConciergeConfigServiceName, "global", "Get",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	assert.False(t, cfg.IsConfigured())
}

func TestGetFullReturnsUnredactedKey(t *testing.T) {
	env := restatetest.Start(t, restate.Reflect(ConciergeConfig{}))
	client := env.Ingress()

	_, err := ingress.Object[ConciergeConfigRequest, restate.Void](
		client, ConciergeConfigServiceName, "global", "Configure",
	).Request(t.Context(), ConciergeConfigRequest{
		Provider: "openai",
		Model:    "gpt-4o",
		APIKey:   "sk-test123",
	})
	require.NoError(t, err)

	cfg, err := ingress.Object[restate.Void, *ConciergeConfiguration](
		client, ConciergeConfigServiceName, "global", "GetFull",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "sk-test123", cfg.APIKey, "GetFull should return unredacted key")
}

func TestConfigureUpdatePreservesFields(t *testing.T) {
	env := restatetest.Start(t, restate.Reflect(ConciergeConfig{}))
	client := env.Ingress()

	// First configure with full settings.
	_, err := ingress.Object[ConciergeConfigRequest, restate.Void](
		client, ConciergeConfigServiceName, "global", "Configure",
	).Request(t.Context(), ConciergeConfigRequest{
		Provider:    "openai",
		Model:       "gpt-4o",
		APIKey:      "sk-test123",
		MaxTurns:    10,
		MaxMessages: 50,
	})
	require.NoError(t, err)

	// Update provider and model, leaving MaxTurns at 0 (should preserve previous).
	_, err = ingress.Object[ConciergeConfigRequest, restate.Void](
		client, ConciergeConfigServiceName, "global", "Configure",
	).Request(t.Context(), ConciergeConfigRequest{
		Provider: "claude",
		Model:    "claude-sonnet-4-20250514",
		APIKey:   "sk-new",
	})
	require.NoError(t, err)

	cfg, err := ingress.Object[restate.Void, ConciergeConfiguration](
		client, ConciergeConfigServiceName, "global", "Get",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	assert.Equal(t, "claude", cfg.Provider)
	assert.Equal(t, "claude-sonnet-4-20250514", cfg.Model)
	assert.Equal(t, 10, cfg.MaxTurns, "should preserve from previous config")
	assert.Equal(t, 50, cfg.MaxMessages, "should preserve from previous config")
}
