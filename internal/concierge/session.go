package concierge

import (
	"fmt"
	"time"

	restate "github.com/restatedev/sdk-go"
)

// ConciergeSession is a Restate Virtual Object keyed by session ID.
type ConciergeSession struct {
	llm      *ProviderRouter
	tools    *ToolRegistry
	migrator *TemplateMigrator
}

func (ConciergeSession) ServiceName() string { return ConciergeSessionServiceName }

// NewConciergeSession creates a session handler with all dependencies wired.
func NewConciergeSession() *ConciergeSession {
	llm := &ProviderRouter{}
	tools := NewToolRegistry()
	migrator := NewTemplateMigrator(llm, tools)
	return &ConciergeSession{
		llm:      llm,
		tools:    tools,
		migrator: migrator,
	}
}

// Ask processes a user prompt through the LLM tool loop.
func (a *ConciergeSession) Ask(ctx restate.ObjectContext, req AskRequest) (AskResponse, error) {
	sessionID := restate.Key(ctx)

	// 1. Load concierge configuration.
	config, err := restate.Object[ConciergeConfiguration](
		ctx, ConciergeConfigServiceName, "global", "Get",
	).Request(restate.Void{})
	if err != nil {
		return AskResponse{}, err
	}
	if !config.IsConfigured() {
		return AskResponse{}, restate.TerminalError(
			fmt.Errorf("agent not configured — run 'praxis concierge configure' first"), 400,
		)
	}

	// Load the unredacted config for API key access.
	fullConfig, err := restate.Object[*ConciergeConfiguration](
		ctx, ConciergeConfigServiceName, "global", "GetFull",
	).Request(restate.Void{})
	if err != nil || fullConfig == nil {
		// Fall back to the redacted config — resolveAPIKey can use apiKeyRef.
		fullConfig = &config
	}
	cfg := fullConfig.Defaults()

	// 2. Capture a durable timestamp.
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return AskResponse{}, err
	}

	// 3. Load or initialize session state.
	statePtr, err := restate.Get[*SessionState](ctx, "state")
	if err != nil {
		return AskResponse{}, err
	}
	var state SessionState
	if statePtr != nil {
		state = *statePtr
	} else {
		state = initSession(req, cfg.SessionTTL, now)

		// Schedule proactive session expiry.
		ttl, _ := time.ParseDuration(cfg.SessionTTL)
		if ttl == 0 {
			ttl = 24 * time.Hour
		}
		restate.ObjectSend(ctx, ConciergeSessionServiceName, sessionID, "Expire").
			Send(restate.Void{}, restate.WithDelay(ttl))
	}

	// Check session expiry.
	if state.ExpiresAt != "" && now > state.ExpiresAt {
		restate.ClearAll(ctx)
		return AskResponse{}, restate.TerminalError(
			fmt.Errorf("session expired — start a new session by omitting --session"), 410,
		)
	}

	// 4. Apply overrides.
	if req.Account != "" {
		state.Account = req.Account
	}
	if req.Workspace != "" {
		state.Workspace = req.Workspace
	}

	// 5. Ensure system prompt.
	if len(state.Messages) == 0 || state.Messages[0].Role != "system" {
		state.Messages = append([]Message{{
			Role:      "system",
			Content:   systemPrompt,
			Timestamp: now,
		}}, state.Messages...)
	}

	// 6. Append user message.
	state.Messages = append(state.Messages, Message{
		Role:      "user",
		Content:   req.Prompt,
		Timestamp: now,
	})

	// 7. Trim history.
	state.Messages = trimHistory(state.Messages, cfg)

	// 8. Resolve API key.
	resolvedKey := cfg.APIKey

	// Set migrator context for this invocation.
	SetMigratorContext(a.migrator, cfg, resolvedKey)

	// 9. Tool loop.
	provider := a.llm.ForConfig(cfg, resolvedKey)
	tools := a.tools.Definitions()

	for turn := 0; turn < cfg.MaxTurns; turn++ {
		state.TurnCount++

		// Call LLM (durable).
		llmResp, err := restate.Run(ctx, func(rc restate.RunContext) (LLMResponse, error) {
			return provider.ChatCompletion(rc, ChatRequest{
				Messages:    state.Messages,
				Tools:       tools,
				Temperature: cfg.Temperature,
			})
		})
		if err != nil {
			return AskResponse{}, err
		}

		// No tool calls → final response.
		if len(llmResp.ToolCalls) == 0 {
			state.Messages = append(state.Messages, Message{
				Role:      "assistant",
				Content:   llmResp.Content,
				Timestamp: now,
			})
			state.LastActiveAt = now
			restate.Set(ctx, "state", state)

			return AskResponse{
				Response:  llmResp.Content,
				SessionID: sessionID,
				TurnCount: state.TurnCount,
			}, nil
		}

		// Append assistant message with tool calls.
		state.Messages = append(state.Messages, Message{
			Role:      "assistant",
			ToolCalls: llmResp.ToolCalls,
			Timestamp: now,
		})

		// Execute each tool call.
		for _, tc := range llmResp.ToolCalls {
			tool := a.tools.Get(tc.Name)
			if tool == nil {
				appendToolError(&state, tc.ID, tc.Name, "unknown tool")
				continue
			}

			if tool.RequiresApproval {
				result, err := a.executeWithApproval(ctx, &state, cfg, tool, tc, now)
				if err != nil {
					appendToolError(&state, tc.ID, tc.Name, err.Error())
				} else {
					appendToolResult(&state, tc.ID, tc.Name, result)
				}
			} else {
				result, err := a.executeTool(ctx, tool, tc, state)
				if err != nil {
					appendToolError(&state, tc.ID, tc.Name, err.Error())
				} else {
					appendToolResult(&state, tc.ID, tc.Name, result)
				}
			}
		}
	}

	// Turn limit reached.
	turnLimitMsg := "I've reached the maximum number of reasoning steps. Here's what I found so far based on the tools I called."
	state.Messages = append(state.Messages, Message{
		Role:      "assistant",
		Content:   turnLimitMsg,
		Timestamp: now,
	})
	state.LastActiveAt = now
	restate.Set(ctx, "state", state)
	return AskResponse{
		Response:  turnLimitMsg,
		SessionID: sessionID,
		TurnCount: state.TurnCount,
	}, nil
}

// executeTool runs a tool and returns its result.
func (a *ConciergeSession) executeTool(ctx restate.Context, tool *ToolDef, tc ToolCall, state SessionState) (string, error) {
	return tool.Execute(ctx, tc.Args, state)
}

// executeWithApproval suspends on an awakeable for human approval.
func (a *ConciergeSession) executeWithApproval(
	ctx restate.ObjectContext,
	state *SessionState,
	config ConciergeConfiguration,
	tool *ToolDef,
	tc ToolCall,
	now string,
) (string, error) {
	awakeable := restate.Awakeable[ApprovalDecision](ctx)
	ttl, _ := time.ParseDuration(config.ApprovalTTL)
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	timer := restate.After(ctx, ttl)

	description := tool.Name
	if tool.DescribeAction != nil {
		description = tool.DescribeAction(tc.Args)
	}

	// Store approval info so transports can discover it via GetStatus.
	state.PendingApproval = &ApprovalInfo{
		AwakeableID: awakeable.Id(),
		Action:      tool.Name,
		Description: description,
		RequestedAt: now,
	}
	restate.Set(ctx, "state", *state)

	// Race: awakeable resolution vs. timeout timer.
	fut, err := restate.WaitFirst(ctx, awakeable, timer)
	if err != nil {
		return "", err
	}

	// Clear pending approval.
	state.PendingApproval = nil
	restate.Set(ctx, "state", *state)

	switch fut {
	case timer:
		if err := timer.Done(); err != nil {
			return "", err
		}
		return fmt.Sprintf("Approval timed out after %s. Action was automatically rejected.", ttl), nil

	case awakeable:
		decision, err := awakeable.Result()
		if err != nil {
			return fmt.Sprintf("Action rejected: %v", err), nil
		}
		if !decision.Approved {
			reason := "User rejected the action"
			if decision.Reason != "" {
				reason = decision.Reason
			}
			return fmt.Sprintf("Action rejected: %s", reason), nil
		}

		// Approved — execute the tool.
		result, err := a.executeTool(ctx, tool, tc, *state)
		if err != nil {
			return "", err
		}
		return result, nil
	}

	return "Unexpected approval state", nil
}

// GetHistory returns the conversation history.
func (a *ConciergeSession) GetHistory(ctx restate.ObjectSharedContext) ([]Message, error) {
	statePtr, err := restate.Get[*SessionState](ctx, "state")
	if err != nil {
		return nil, err
	}
	if statePtr == nil {
		return nil, nil
	}
	return statePtr.Messages, nil
}

// GetStatus returns session metadata.
func (a *ConciergeSession) GetStatus(ctx restate.ObjectSharedContext) (SessionStatus, error) {
	statePtr, err := restate.Get[*SessionState](ctx, "state")
	if err != nil {
		return SessionStatus{}, err
	}
	if statePtr == nil {
		return SessionStatus{}, nil
	}
	return SessionStatus{
		Provider:        statePtr.Provider,
		Model:           statePtr.Model,
		TurnCount:       statePtr.TurnCount,
		LastActiveAt:    statePtr.LastActiveAt,
		ExpiresAt:       statePtr.ExpiresAt,
		PendingApproval: statePtr.PendingApproval,
	}, nil
}

// Reset clears conversation history and state.
func (a *ConciergeSession) Reset(ctx restate.ObjectContext) error {
	restate.ClearAll(ctx)
	return nil
}

// Expire proactively cleans up expired sessions.
func (a *ConciergeSession) Expire(ctx restate.ObjectContext) error {
	statePtr, err := restate.Get[*SessionState](ctx, "state")
	if err != nil {
		return err
	}
	if statePtr == nil {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if statePtr.ExpiresAt != "" && now >= statePtr.ExpiresAt {
		restate.ClearAll(ctx)
		return nil
	}

	// Session was extended — re-schedule.
	expiresAt, err := time.Parse(time.RFC3339, statePtr.ExpiresAt)
	if err != nil {
		restate.ClearAll(ctx)
		return nil
	}
	remaining := time.Until(expiresAt)
	if remaining > 0 {
		restate.ObjectSend(ctx, ConciergeSessionServiceName, restate.Key(ctx), "Expire").
			Send(restate.Void{}, restate.WithDelay(remaining))
	} else {
		restate.ClearAll(ctx)
	}
	return nil
}

func appendToolResult(state *SessionState, toolCallID, toolName, result string) {
	state.Messages = append(state.Messages, Message{
		Role:       "tool",
		Content:    result,
		ToolCallID: toolCallID,
		Name:       toolName,
	})
}

func appendToolError(state *SessionState, toolCallID, toolName, errMsg string) {
	state.Messages = append(state.Messages, Message{
		Role:       "tool",
		Content:    fmt.Sprintf("Error: %s", errMsg),
		ToolCallID: toolCallID,
		Name:       toolName,
	})
}
