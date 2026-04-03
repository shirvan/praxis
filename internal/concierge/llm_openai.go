package concierge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// OpenAIProvider calls any OpenAI-compatible chat completions API. This includes
// the official OpenAI API, Azure OpenAI, and self-hosted models that expose an
// OpenAI-compatible endpoint (e.g., vLLM, Ollama). The baseURL can be overridden
// in ConciergeConfiguration to point at any compatible endpoint.
type OpenAIProvider struct {
	baseURL    string       // API base URL (default: https://api.openai.com/v1)
	apiKey     string       // Bearer token for Authorization header
	model      string       // Model name (e.g., "gpt-4o", "gpt-4o-mini")
	httpClient *http.Client // Optional custom HTTP client (for testing)
}

// openAIRequest is the OpenAI chat completions request body.
// See: https://platform.openai.com/docs/api-reference/chat/create
type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Tools       []openAITool    `json:"tools,omitempty"`
	ToolChoice  any             `json:"tool_choice,omitempty"`
	Temperature float64         `json:"temperature"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// openAIResponse is the OpenAI chat completions response body.
type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
	Usage   openAIUsage    `json:"usage"`
	Error   *openAIError   `json:"error,omitempty"`
}

type openAIChoice struct {
	Message openAIMessage `json:"message"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

func (o *OpenAIProvider) client() *http.Client {
	if o.httpClient != nil {
		return o.httpClient
	}
	return http.DefaultClient
}

// ChatCompletion sends a chat request to an OpenAI-compatible API.
// Translates the provider-agnostic ChatRequest to OpenAI's wire format,
// makes the HTTP call, and translates the response back. Tool calls are
// mapped from OpenAI's function_call format to our ToolCall struct.
func (o *OpenAIProvider) ChatCompletion(ctx context.Context, req ChatRequest) (LLMResponse, error) {
	oaiReq := openAIRequest{
		Model:       o.model,
		Messages:    toOpenAIMessages(req.Messages),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	if len(req.Tools) > 0 {
		oaiReq.Tools = toOpenAITools(req.Tools)
		oaiReq.ToolChoice = "auto"
	}

	body, err := json.Marshal(oaiReq)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("marshal openai request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return LLMResponse{}, fmt.Errorf("create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.client().Do(httpReq) //nolint:gosec // G704 URL is from configured API endpoint
	if err != nil {
		return LLMResponse{}, fmt.Errorf("openai request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return LLMResponse{}, fmt.Errorf("openai returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		return LLMResponse{}, fmt.Errorf("unmarshal openai response: %w", err)
	}

	if oaiResp.Error != nil {
		return LLMResponse{}, fmt.Errorf("openai error: %s", oaiResp.Error.Message)
	}

	if len(oaiResp.Choices) == 0 {
		return LLMResponse{}, fmt.Errorf("openai returned no choices")
	}

	choice := oaiResp.Choices[0]
	result := LLMResponse{
		Content: choice.Message.Content,
		Usage: Usage{
			PromptTokens:     oaiResp.Usage.PromptTokens,
			CompletionTokens: oaiResp.Usage.CompletionTokens,
			TotalTokens:      oaiResp.Usage.TotalTokens,
		},
	}

	for _, tc := range choice.Message.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: tc.Function.Arguments,
		})
	}

	return result, nil
}

// toOpenAIMessages converts provider-agnostic Messages to OpenAI's message format.
// Tool calls and tool results are mapped to OpenAI's tool_calls and tool role messages.
func toOpenAIMessages(msgs []Message) []openAIMessage {
	out := make([]openAIMessage, 0, len(msgs))
	for _, m := range msgs {
		oai := openAIMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		for _, tc := range m.ToolCalls {
			oai.ToolCalls = append(oai.ToolCalls, openAIToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: openAIFunctionCall{
					Name:      tc.Name,
					Arguments: tc.Args,
				},
			})
		}
		out = append(out, oai)
	}
	return out
}

// toOpenAITools converts provider-agnostic ToolSchemas to OpenAI's function tool format.
func toOpenAITools(tools []ToolSchema) []openAITool {
	out := make([]openAITool, 0, len(tools))
	for _, t := range tools {
		out = append(out, openAITool{
			Type:     "function",
			Function: openAIFunction(t),
		})
	}
	return out
}
