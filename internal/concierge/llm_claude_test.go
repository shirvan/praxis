package concierge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeProviderChatCompletion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
		assert.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"))

		var req claudeRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "claude-sonnet-4-20250514", req.Model)
		assert.Equal(t, "You are helpful", req.System)

		resp := claudeResponse{
			Content: []claudeContentBlock{
				{Type: "text", Text: "Hello from Claude"},
			},
			Usage:      claudeUsage{InputTokens: 10, OutputTokens: 5},
			StopReason: "end_turn",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	provider := &ClaudeProvider{
		apiKey:     "test-key",
		model:      "claude-sonnet-4-20250514",
		httpClient: srv.Client(),
	}

	// Temporarily override the API URL for testing.
	origURL := claudeAPIURL
	defer func() { overrideClaudeURL = "" }()
	overrideClaudeURL = srv.URL + "/v1/messages"

	resp, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: "system", Content: "You are helpful"},
			{Role: "user", Content: "hi"},
		},
		Temperature: 0.1,
	})
	require.NoError(t, err)
	assert.Equal(t, "Hello from Claude", resp.Content)
	assert.Equal(t, 15, resp.Usage.TotalTokens)
	_ = origURL
}

func TestClaudeProviderToolUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := claudeResponse{
			Content: []claudeContentBlock{
				{Type: "text", Text: "Let me check that"},
				{
					Type:  "tool_use",
					ID:    "toolu_123",
					Name:  "listDeployments",
					Input: json.RawMessage(`{"account":"prod"}`),
				},
			},
			Usage:      claudeUsage{InputTokens: 20, OutputTokens: 15},
			StopReason: "tool_use",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	provider := &ClaudeProvider{
		apiKey:     "key",
		model:      "claude-sonnet-4-20250514",
		httpClient: srv.Client(),
	}
	overrideClaudeURL = srv.URL + "/v1/messages"
	defer func() { overrideClaudeURL = "" }()

	resp, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "list deployments"}},
		Tools: []ToolSchema{{
			Name:        "listDeployments",
			Description: "List deployments",
			Parameters:  map[string]any{"type": "object"},
		}},
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "Let me check that")
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "toolu_123", resp.ToolCalls[0].ID)
	assert.Equal(t, "listDeployments", resp.ToolCalls[0].Name)
}

func TestClaudeProviderErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"type":"authentication_error","message":"invalid key"}}`))
	}))
	defer srv.Close()

	provider := &ClaudeProvider{
		apiKey:     "bad-key",
		model:      "claude-sonnet-4-20250514",
		httpClient: srv.Client(),
	}
	overrideClaudeURL = srv.URL + "/v1/messages"
	defer func() { overrideClaudeURL = "" }()

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestToClaudeMessagesExtractsSystem(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	claudeMsgs, system := toClaudeMessages(msgs)

	assert.Equal(t, "system prompt", system)
	assert.Len(t, claudeMsgs, 2)
	assert.Equal(t, "user", claudeMsgs[0].Role)
	assert.Equal(t, "assistant", claudeMsgs[1].Role)
}

func TestToClaudeMessagesToolResult(t *testing.T) {
	msgs := []Message{
		{Role: "tool", Content: "result data", ToolCallID: "call_1"},
	}
	claudeMsgs, _ := toClaudeMessages(msgs)

	require.Len(t, claudeMsgs, 1)
	assert.Equal(t, "user", claudeMsgs[0].Role)

	var blocks []map[string]any
	require.NoError(t, json.Unmarshal(claudeMsgs[0].Content, &blocks))
	require.Len(t, blocks, 1)
	assert.Equal(t, "tool_result", blocks[0]["type"])
}
