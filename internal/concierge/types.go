package concierge

import "time"

const (
	ConciergeSessionServiceName = "ConciergeSession"
	ConciergeConfigServiceName  = "ConciergeConfig"
	ApprovalRelayServiceName    = "ApprovalRelay"
)

// SessionState is the single atomic state object stored per session.
type SessionState struct {
	Messages        []Message     `json:"messages"`
	Provider        string        `json:"provider"`
	Model           string        `json:"model"`
	Account         string        `json:"account,omitempty"`
	Workspace       string        `json:"workspace,omitempty"`
	CreatedAt       string        `json:"createdAt"`
	LastActiveAt    string        `json:"lastActiveAt"`
	ExpiresAt       string        `json:"expiresAt"`
	TurnCount       int           `json:"turnCount"`
	PendingApproval *ApprovalInfo `json:"pendingApproval,omitempty"`
}

// Message represents a single message in the conversation.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	ToolCallID string     `json:"toolCallId,omitempty"`
	Name       string     `json:"name,omitempty"`
	Timestamp  string     `json:"timestamp"`
}

// ToolCall represents an LLM-requested tool invocation.
type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

// ApprovalInfo tracks a pending human approval.
type ApprovalInfo struct {
	AwakeableID string `json:"awakeableId"`
	Action      string `json:"action"`
	Description string `json:"description"`
	RequestedAt string `json:"requestedAt"`
}

// SessionStatus is returned by GetStatus.
type SessionStatus struct {
	Provider        string        `json:"provider"`
	Model           string        `json:"model"`
	TurnCount       int           `json:"turnCount"`
	LastActiveAt    string        `json:"lastActiveAt"`
	ExpiresAt       string        `json:"expiresAt"`
	PendingApproval *ApprovalInfo `json:"pendingApproval,omitempty"`
}

// AskRequest is the input to the Ask handler.
type AskRequest struct {
	Prompt    string `json:"prompt"`
	Account   string `json:"account,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Source    string `json:"source,omitempty"`
}

// AskResponse is returned by the Ask handler.
type AskResponse struct {
	Response  string `json:"response"`
	SessionID string `json:"sessionId"`
	TurnCount int    `json:"turnCount"`
}

// ApprovalDecision is the input to ApprovalRelay.Resolve.
type ApprovalDecision struct {
	Approved bool   `json:"approved"`
	Reason   string `json:"reason,omitempty"`
}

// ApprovalRelayRequest is the input to ApprovalRelay.Resolve.
type ApprovalRelayRequest struct {
	AwakeableID string `json:"awakeableId"`
	Approved    bool   `json:"approved"`
	Reason      string `json:"reason,omitempty"`
	Actor       string `json:"actor,omitempty"`
}

// ConciergeConfiguration holds the LLM provider settings.
type ConciergeConfiguration struct {
	Provider    string  `json:"provider"`
	Model       string  `json:"model"`
	APIKey      string  `json:"apiKey,omitempty"`
	APIKeyRef   string  `json:"apiKeyRef,omitempty"`
	BaseURL     string  `json:"baseURL,omitempty"`
	MaxTurns    int     `json:"maxTurns"`
	MaxMessages int     `json:"maxMessages"`
	Temperature float64 `json:"temperature"`
	SessionTTL  string  `json:"sessionTTL"`
	ApprovalTTL string  `json:"approvalTTL"`
}

// ConciergeConfigRequest is the input to ConciergeConfig.Configure.
type ConciergeConfigRequest struct {
	Provider    string  `json:"provider"`
	Model       string  `json:"model"`
	APIKey      string  `json:"apiKey,omitempty"`
	APIKeyRef   string  `json:"apiKeyRef,omitempty"`
	BaseURL     string  `json:"baseURL,omitempty"`
	MaxTurns    int     `json:"maxTurns,omitempty"`
	MaxMessages int     `json:"maxMessages,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	SessionTTL  string  `json:"sessionTTL,omitempty"`
	ApprovalTTL string  `json:"approvalTTL,omitempty"`
}

// Defaults returns a copy with zero-value fields filled in.
func (c ConciergeConfiguration) Defaults() ConciergeConfiguration {
	if c.MaxTurns == 0 {
		c.MaxTurns = 20
	}
	if c.MaxMessages == 0 {
		c.MaxMessages = 200
	}
	if c.Temperature == 0 {
		c.Temperature = 0.1
	}
	if c.SessionTTL == "" {
		c.SessionTTL = "24h"
	}
	if c.ApprovalTTL == "" {
		c.ApprovalTTL = "5m"
	}
	return c
}

// IsConfigured returns true if a provider and model are set.
func (c ConciergeConfiguration) IsConfigured() bool {
	return c.Provider != "" && c.Model != ""
}

// Redacted returns a copy with API keys masked.
func (c ConciergeConfiguration) Redacted() ConciergeConfiguration {
	out := c
	if out.APIKey != "" {
		out.APIKey = "***"
	}
	return out
}

// initSession creates a new SessionState for a first-time session.
func initSession(req AskRequest, sessionTTL, now string) SessionState {
	ttl, _ := time.ParseDuration(sessionTTL)
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	nowT, _ := time.Parse(time.RFC3339, now)
	return SessionState{
		Account:      req.Account,
		Workspace:    req.Workspace,
		CreatedAt:    now,
		LastActiveAt: now,
		ExpiresAt:    nowT.Add(ttl).Format(time.RFC3339),
	}
}
