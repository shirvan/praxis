package concierge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const claudeAPIURL = "https://api.anthropic.com/v1/messages"

// overrideClaudeURL is used during tests to point at a local httptest server.
var overrideClaudeURL string

func claudeEndpoint() string {
	if overrideClaudeURL != "" {
		return overrideClaudeURL
	}
	return claudeAPIURL
}

// ClaudeProvider calls the Anthropic Messages API. Claude has a different
// message format than OpenAI:
//   - System prompts are a top-level field, not a message role
//   - Tool results use "user" role with tool_result content blocks
//   - Tool calls are "tool_use" content blocks on assistant messages
//   - Content is always an array of content blocks (text, tool_use, tool_result)
//
// This provider handles all the format translation between our provider-agnostic
// Message format and Claude's native wire format.
type ClaudeProvider struct {
	apiKey     string       // Anthropic API key (x-api-key header)
	model      string       // Model name (e.g., "claude-sonnet-4-20250514")
	httpClient *http.Client // Optional custom HTTP client (for testing)
}

type claudeRequest struct {
	Model       string          `json:"model"`
	MaxTokens   int             `json:"max_tokens"`
	System      string          `json:"system,omitempty"`
	Messages    []claudeMessage `json:"messages"`
	Tools       []claudeTool    `json:"tools,omitempty"`
	Temperature float64         `json:"temperature"`
}

type claudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type claudeTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type claudeResponse struct {
	Content    []claudeContentBlock `json:"content"`
	Usage      claudeUsage          `json:"usage"`
	Error      *claudeError         `json:"error,omitempty"`
	StopReason string               `json:"stop_reason"`
}

type claudeContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type claudeError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (c *ClaudeProvider) client() *http.Client {
	if c.httpClient != nil {
		return c.httpClient
	}
	return http.DefaultClient
}

// ChatCompletion sends a chat request to the Anthropic Claude API.
// Translates the provider-agnostic ChatRequest to Claude's Messages API format,
// makes the HTTP call, and translates the response back. System prompts are
// extracted from the message list and sent as a top-level field.
func (c *ClaudeProvider) ChatCompletion(ctx context.Context, req ChatRequest) (LLMResponse, error) {
	clReq := claudeRequest{
		Model:       c.model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}
	if clReq.MaxTokens == 0 {
		clReq.MaxTokens = 4096
	}

	// Extract system prompt and convert messages.
	clReq.Messages, clReq.System = toClaudeMessages(req.Messages)

	if len(req.Tools) > 0 {
		clReq.Tools = toClaudeTools(req.Tools)
	}

	body, err := json.Marshal(clReq)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("marshal claude request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeEndpoint(), bytes.NewReader(body))
	if err != nil {
		return LLMResponse{}, fmt.Errorf("create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.client().Do(httpReq) //nolint:gosec // G704 URL is from configured API endpoint
	if err != nil {
		return LLMResponse{}, fmt.Errorf("claude request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("returned status %d", resp.StatusCode)
		var errResp claudeResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != nil {
			msg = errResp.Error.Message
		}
		return LLMResponse{}, &LLMError{
			StatusCode: resp.StatusCode,
			Provider:   "claude",
			Message:    msg,
		}
	}

	var clResp claudeResponse
	if err := json.Unmarshal(respBody, &clResp); err != nil {
		return LLMResponse{}, fmt.Errorf("unmarshal claude response: %w", err)
	}

	if clResp.Error != nil {
		return LLMResponse{}, fmt.Errorf("claude error [%s]: %s", clResp.Error.Type, clResp.Error.Message)
	}

	result := LLMResponse{
		Usage: Usage{
			PromptTokens:     clResp.Usage.InputTokens,
			CompletionTokens: clResp.Usage.OutputTokens,
			TotalTokens:      clResp.Usage.InputTokens + clResp.Usage.OutputTokens,
		},
	}

	for _, block := range clResp.Content {
		switch block.Type {
		case "text":
			result.Content += block.Text
		case "tool_use":
			args, _ := json.Marshal(block.Input)
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:   block.ID,
				Name: block.Name,
				Args: string(args),
			})
		}
	}

	return result, nil
}

// toClaudeMessages converts provider-agnostic Messages to Claude's format.
// Key differences from OpenAI:
//   - System messages are extracted and returned separately (Claude uses a top-level system field)
//   - Tool results use "user" role with tool_result content blocks (not a "tool" role)
//   - Assistant messages with tool calls become content block arrays with tool_use entries
//   - Regular text messages have their content JSON-encoded as a string
func toClaudeMessages(msgs []Message) ([]claudeMessage, string) {
	var system string
	var out []claudeMessage

	for _, m := range msgs {
		if m.Role == "system" {
			system = m.Content
			continue
		}

		role := m.Role
		if role == "tool" {
			// Claude uses "user" role with tool_result content blocks.
			content := []map[string]any{{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Content,
			}}
			raw, _ := json.Marshal(content)
			out = append(out, claudeMessage{Role: "user", Content: raw})
			continue
		}

		if role == "assistant" && len(m.ToolCalls) > 0 {
			// Assistant message with tool_use blocks.
			var blocks []map[string]any
			if m.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				var input any
				_ = json.Unmarshal([]byte(tc.Args), &input)
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Name,
					"input": input,
				})
			}
			raw, _ := json.Marshal(blocks)
			out = append(out, claudeMessage{Role: "assistant", Content: raw})
			continue
		}

		// Standard text message.
		raw, _ := json.Marshal(m.Content)
		out = append(out, claudeMessage{Role: role, Content: raw})
	}

	return out, system
}

// toClaudeTools converts provider-agnostic ToolSchemas to Claude's tool format.
// Claude uses "input_schema" instead of OpenAI's "parameters".
func toClaudeTools(tools []ToolSchema) []claudeTool {
	out := make([]claudeTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, claudeTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
	}
	return out
}
