package concierge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMockLLMServer creates an httptest server that returns a fixed text response.
func newMockLLMServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openAIResponse{
			Choices: []openAIChoice{{
				Message: openAIMessage{Role: "assistant", Content: content},
			}},
			Usage: openAIUsage{TotalTokens: 10},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

func startTestEnv(t *testing.T, llmServerURL string) (*restatetest.TestEnvironment, *ConciergeSession) {
	t.Helper()

	session := NewConciergeSession()

	env := restatetest.Start(t,
		restate.Reflect(session),
		restate.Reflect(ConciergeConfig{}),
		restate.Reflect(ApprovalRelay{}),
		restate.Reflect(ConciergeProgress{}),
	)

	// Configure the concierge with the mock LLM.
	_, err := ingress.Object[ConciergeConfigRequest, restate.Void](
		env.Ingress(), ConciergeConfigServiceName, "global", "Configure",
	).Request(t.Context(), ConciergeConfigRequest{
		Provider: "openai",
		Model:    "gpt-4o",
		APIKey:   "test-key",
		BaseURL:  llmServerURL,
	})
	require.NoError(t, err)

	return env, session
}

func TestSessionAskSimpleResponse(t *testing.T) {
	llm := newMockLLMServer(t, "Hello! I'm the Praxis assistant.")
	defer llm.Close()

	env, _ := startTestEnv(t, llm.URL)

	resp, err := ingress.Object[AskRequest, AskResponse](
		env.Ingress(), ConciergeSessionServiceName, "test-session-1", "Ask",
	).Request(t.Context(), AskRequest{Prompt: "hi"})
	require.NoError(t, err)

	assert.Equal(t, "Hello! I'm the Praxis assistant.", resp.Response)
	assert.Equal(t, "test-session-1", resp.SessionID)
	assert.Equal(t, 1, resp.TurnCount)
}

func TestSessionGetStatusAfterAsk(t *testing.T) {
	llm := newMockLLMServer(t, "response")
	defer llm.Close()

	env, _ := startTestEnv(t, llm.URL)

	_, err := ingress.Object[AskRequest, AskResponse](
		env.Ingress(), ConciergeSessionServiceName, "status-session", "Ask",
	).Request(t.Context(), AskRequest{Prompt: "hello"})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, SessionStatus](
		env.Ingress(), ConciergeSessionServiceName, "status-session", "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	assert.Equal(t, 1, status.TurnCount)
	assert.NotEmpty(t, status.LastActiveAt)
	assert.NotEmpty(t, status.ExpiresAt)
	assert.Nil(t, status.PendingApproval)
}

func TestSessionGetHistory(t *testing.T) {
	llm := newMockLLMServer(t, "the answer")
	defer llm.Close()

	env, _ := startTestEnv(t, llm.URL)

	_, err := ingress.Object[AskRequest, AskResponse](
		env.Ingress(), ConciergeSessionServiceName, "history-session", "Ask",
	).Request(t.Context(), AskRequest{Prompt: "question"})
	require.NoError(t, err)

	history, err := ingress.Object[restate.Void, []Message](
		env.Ingress(), ConciergeSessionServiceName, "history-session", "GetHistory",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	// Should contain: system prompt, user message, assistant response.
	require.GreaterOrEqual(t, len(history), 3)
	assert.Equal(t, "system", history[0].Role)
	assert.Equal(t, "user", history[1].Role)
	assert.Equal(t, "question", history[1].Content)
	assert.Equal(t, "assistant", history[2].Role)
	assert.Equal(t, "the answer", history[2].Content)
}

func TestSessionReset(t *testing.T) {
	llm := newMockLLMServer(t, "response")
	defer llm.Close()

	env, _ := startTestEnv(t, llm.URL)

	_, err := ingress.Object[AskRequest, AskResponse](
		env.Ingress(), ConciergeSessionServiceName, "reset-session", "Ask",
	).Request(t.Context(), AskRequest{Prompt: "hello"})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		env.Ingress(), ConciergeSessionServiceName, "reset-session", "Reset",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, SessionStatus](
		env.Ingress(), ConciergeSessionServiceName, "reset-session", "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	assert.Equal(t, 0, status.TurnCount)
}

func TestSessionMultipleAsks(t *testing.T) {
	llm := newMockLLMServer(t, "response")
	defer llm.Close()

	env, _ := startTestEnv(t, llm.URL)

	_, err := ingress.Object[AskRequest, AskResponse](
		env.Ingress(), ConciergeSessionServiceName, "multi-session", "Ask",
	).Request(t.Context(), AskRequest{Prompt: "first"})
	require.NoError(t, err)

	resp, err := ingress.Object[AskRequest, AskResponse](
		env.Ingress(), ConciergeSessionServiceName, "multi-session", "Ask",
	).Request(t.Context(), AskRequest{Prompt: "second"})
	require.NoError(t, err)

	assert.Equal(t, 2, resp.TurnCount, "should accumulate turns")
}

func TestSessionNotConfigured(t *testing.T) {
	session := NewConciergeSession()

	env := restatetest.Start(t,
		restate.Reflect(session),
		restate.Reflect(ConciergeConfig{}),
		restate.Reflect(ApprovalRelay{}),
		restate.Reflect(ConciergeProgress{}),
	)

	_, err := ingress.Object[AskRequest, AskResponse](
		env.Ingress(), ConciergeSessionServiceName, "test-not-configured", "Ask",
	).Request(t.Context(), AskRequest{Prompt: "hi"})
	assert.Error(t, err)
}
