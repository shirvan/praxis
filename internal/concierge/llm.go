package concierge

import "context"

// LLMProvider is the interface that all LLM backends implement. The Concierge
// uses this abstraction to support multiple providers (OpenAI, Claude) behind
// a common API. Implementations handle provider-specific wire formats, auth
// headers, and response parsing.
//
// Important: ChatCompletion receives a plain context.Context, NOT a Restate
// context. This is because LLM calls are always wrapped in restate.Run() by
// the caller (ConciergeSession.Ask), which provides a RunContext that satisfies
// context.Context. The restate.Run() wrapper ensures the LLM response is
// journaled for deterministic replay — on retry, Restate returns the cached
// response instead of calling the LLM API again.
type LLMProvider interface {
	// ChatCompletion sends a chat request and returns the response.
	// This is called inside restate.Run() by the caller — implementations
	// must NOT use Restate context internally.
	ChatCompletion(ctx context.Context, req ChatRequest) (LLMResponse, error)
}

// ChatRequest is the provider-agnostic request format sent to any LLM backend.
// The session constructs this from the conversation history and registered tool
// schemas, then the provider translates it to its native wire format.
type ChatRequest struct {
	Messages    []Message    `json:"messages"`
	Tools       []ToolSchema `json:"tools"`
	Temperature float64      `json:"temperature"`
	MaxTokens   int          `json:"maxTokens,omitempty"`
}

// LLMResponse is the provider-agnostic response format returned by any LLM backend.
// If ToolCalls is non-empty, the LLM is requesting tool execution and the session
// must process them before calling the LLM again. If ToolCalls is empty, Content
// contains the final text response for the user.
type LLMResponse struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
	Usage     Usage      `json:"usage"`
}

// ToolSchema is the JSON Schema description of a tool, sent to the LLM so it
// knows what tools are available and what arguments they accept. Each registered
// tool in the ToolRegistry produces a ToolSchema via Definitions().
type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Usage tracks token consumption for observability. Populated by providers
// from their native usage statistics.
type Usage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// ProviderRouter selects the appropriate LLM provider based on configuration.
// It is stateless — it creates a new provider instance for each request based
// on the current config. This allows hot-switching providers by just updating
// the ConciergeConfig.
type ProviderRouter struct{}

// ForConfig returns an LLMProvider for the given config and resolved API key.
// The provider is a fresh instance; no state is shared across requests.
// Defaults to OpenAI if the provider name is unrecognized.
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
