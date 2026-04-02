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

func TestOpenAIProviderChatCompletion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req openAIRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "gpt-4o", req.Model)
		assert.Len(t, req.Messages, 1)

		resp := openAIResponse{
			Choices: []openAIChoice{{
				Message: openAIMessage{Role: "assistant", Content: "Hello from OpenAI"},
			}},
			Usage: openAIUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	provider := &OpenAIProvider{
		baseURL:    srv.URL,
		apiKey:     "test-key",
		model:      "gpt-4o",
		httpClient: srv.Client(),
	}

	resp, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Messages:    []Message{{Role: "user", Content: "hi"}},
		Temperature: 0.1,
	})
	require.NoError(t, err)
	assert.Equal(t, "Hello from OpenAI", resp.Content)
	assert.Equal(t, 15, resp.Usage.TotalTokens)
	assert.Empty(t, resp.ToolCalls)
}

func TestOpenAIProviderToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openAIResponse{
			Choices: []openAIChoice{{
				Message: openAIMessage{
					Role: "assistant",
					ToolCalls: []openAIToolCall{{
						ID:   "call_123",
						Type: "function",
						Function: openAIFunctionCall{
							Name:      "listDeployments",
							Arguments: `{"account":"prod"}`,
						},
					}},
				},
			}},
			Usage: openAIUsage{TotalTokens: 20},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	provider := &OpenAIProvider{
		baseURL:    srv.URL,
		apiKey:     "key",
		model:      "gpt-4o",
		httpClient: srv.Client(),
	}

	resp, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "list my deployments"}},
		Tools: []ToolSchema{{
			Name:        "listDeployments",
			Description: "List deployments",
			Parameters:  map[string]any{"type": "object"},
		}},
	})
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_123", resp.ToolCalls[0].ID)
	assert.Equal(t, "listDeployments", resp.ToolCalls[0].Name)
}

func TestOpenAIProviderErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	provider := &OpenAIProvider{
		baseURL:    srv.URL,
		apiKey:     "key",
		model:      "gpt-4o",
		httpClient: srv.Client(),
	}

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "429")
}

func TestOpenAIProviderNoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openAIResponse{Choices: []openAIChoice{}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	provider := &OpenAIProvider{
		baseURL:    srv.URL,
		apiKey:     "key",
		model:      "gpt-4o",
		httpClient: srv.Client(),
	}

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no choices")
}
