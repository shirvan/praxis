package concierge

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProviderRouterOpenAI(t *testing.T) {
	r := &ProviderRouter{}
	cfg := ConciergeConfiguration{Provider: "openai", Model: "gpt-4o"}
	p := r.ForConfig(cfg, "test-key")
	_, ok := p.(*OpenAIProvider)
	assert.True(t, ok)
}

func TestProviderRouterOpenAIWithBaseURL(t *testing.T) {
	r := &ProviderRouter{}
	cfg := ConciergeConfiguration{Provider: "openai", Model: "gpt-4o", BaseURL: "http://localhost:11434/v1"}
	p := r.ForConfig(cfg, "key")
	oai, ok := p.(*OpenAIProvider)
	assert.True(t, ok)
	assert.Equal(t, "http://localhost:11434/v1", oai.baseURL)
}

func TestProviderRouterClaude(t *testing.T) {
	r := &ProviderRouter{}
	cfg := ConciergeConfiguration{Provider: "claude", Model: "claude-sonnet-4-20250514"}
	p := r.ForConfig(cfg, "test-key")
	_, ok := p.(*ClaudeProvider)
	assert.True(t, ok)
}

func TestProviderRouterDefaultFallback(t *testing.T) {
	r := &ProviderRouter{}
	cfg := ConciergeConfiguration{Provider: "unknown", Model: "some-model"}
	p := r.ForConfig(cfg, "key")
	_, ok := p.(*OpenAIProvider)
	assert.True(t, ok, "unknown provider should fall back to OpenAI-compatible")
}
