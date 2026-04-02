package concierge

import "context"

// LLMProvider is the interface that all LLM backends implement.
type LLMProvider interface {
	// ChatCompletion sends a chat request and returns the response.
	// This is called inside restate.Run() by the caller — implementations
	// must NOT use Restate context internally.
	ChatCompletion(ctx context.Context, req ChatRequest) (LLMResponse, error)
}

// ChatRequest is the provider-agnostic request format.
type ChatRequest struct {
	Messages    []Message    `json:"messages"`
	Tools       []ToolSchema `json:"tools"`
	Temperature float64      `json:"temperature"`
	MaxTokens   int          `json:"maxTokens,omitempty"`
}

// LLMResponse is the provider-agnostic response format.
type LLMResponse struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
	Usage     Usage      `json:"usage"`
}

// ToolSchema is the JSON Schema description of a tool for the LLM.
type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Usage tracks token consumption for observability.
type Usage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// ProviderRouter selects the appropriate LLM provider based on configuration.
type ProviderRouter struct{}

// ForConfig returns an LLMProvider for the given config and resolved API key.
func (r *ProviderRouter) ForConfig(cfg ConciergeConfiguration, resolvedKey string) LLMProvider {
	switch cfg.Provider {
	case "claude":
		return &ClaudeProvider{apiKey: resolvedKey, model: cfg.Model}
	case "openai":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		return &OpenAIProvider{baseURL: baseURL, apiKey: resolvedKey, model: cfg.Model}
	default:
		return &OpenAIProvider{baseURL: "https://api.openai.com/v1", apiKey: resolvedKey, model: cfg.Model}
	}
}
