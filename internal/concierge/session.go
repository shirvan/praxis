package concierge

import (
	"fmt"
	"time"

	restate "github.com/restatedev/sdk-go"
)

// ConciergeSession is the main Restate Virtual Object that implements the AI
// conversation loop. Each instance is keyed by a unique session ID, giving us:
//   - Durable state: conversation history persists across crashes and restarts
//   - Single-writer concurrency: Restate ensures only one Ask() executes per session
//     at a time, preventing race conditions on shared conversation state
//   - Automatic lifecycle: sessions self-expire via delayed Restate messages
//
// The session holds references to the LLM provider router, tool registry, and
// template migrator — all stateless collaborators wired at construction time.
type ConciergeSession struct {
	llm      *ProviderRouter   // Selects OpenAI or Claude based on config
	tools    *ToolRegistry     // All available tools (read, write, explain, migrate)
	migrator *TemplateMigrator // Orchestrates Terraform/CloudFormation/Crossplane → CUE conversion
}

// ServiceName returns the Restate service name used for registration and cross-service calls.
func (ConciergeSession) ServiceName() string { return ConciergeSessionServiceName }

// NewConciergeSession creates a session handler with all dependencies wired.
// This is called once at startup; the returned handler is registered with the
// Restate server and handles all session keys.
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

// Ask processes a user prompt through the LLM tool loop. This is the primary
// entry point for all user interaction with the Concierge.
//
// The handler implements an agentic tool-calling loop:
//  1. Load LLM configuration from ConciergeConfig Virtual Object
//  2. Capture a durable timestamp via restate.Run() (deterministic on replay)
//  3. Load or initialize session state from Restate's KV store
//  4. Append user message to conversation history
//  5. Enter tool loop: call LLM → execute tool calls → repeat
//  6. When LLM returns text without tool calls, return to user
//
// The tool loop distinguishes read-only tools (execute immediately) from write
// tools (suspend on a Restate awakeable for human approval). The entire loop
// runs within Restate's durable execution — if the process crashes mid-loop,
// it resumes from the last completed journal entry.
func (a *ConciergeSession) Ask(ctx restate.ObjectContext, req AskRequest) (AskResponse, error) {
	sessionID := restate.Key(ctx)

	// 1. Load concierge configuration from the ConciergeConfig Virtual Object.
	// This cross-service call is durable — the result is journaled by Restate.
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

	// Load the unredacted config for API key access. The Get handler returns a
	// redacted copy (secrets masked), so we call GetFull for service-to-service use.
	fullConfig, err := restate.Object[*ConciergeConfiguration](
		ctx, ConciergeConfigServiceName, "global", "GetFull",
	).Request(restate.Void{})
	if err != nil || fullConfig == nil {
		// Fall back to the redacted config — resolveAPIKey can use apiKeyRef.
		fullConfig = &config
	}
	cfg := fullConfig.Defaults()

	// 2. Capture a durable timestamp. Using restate.Run() ensures the timestamp
	// is recorded in the journal and replayed deterministically on retries.
	// Never use time.Now() directly in a Restate handler — it would return
	// different values on replay.
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return AskResponse{}, err
	}

	// 3. Load or initialize session state from Restate's durable KV store.
	// On the first call to a session, statePtr will be nil and we create
	// fresh state. On subsequent calls, we resume the existing conversation.
	statePtr, err := restate.Get[*SessionState](ctx, "state")
	if err != nil {
		return AskResponse{}, err
	}
	var state SessionState
	if statePtr != nil {
		state = *statePtr
	} else {
		state = initSession(req, cfg.SessionTTL, now)
		state.Provider = cfg.Provider
		state.Model = cfg.Model

		// Schedule proactive session expiry using a delayed self-message.
		// This is a Restate durable timer — it survives process restarts.
		// When the timer fires, the Expire handler checks if the session
		// is still expired and clears state if so.
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

	// 7. Trim history to stay within token/message limits.
	// Preserves the system prompt and keeps the most recent messages.
	state.Messages = trimHistory(state.Messages, cfg)

	// 8. Resolve API key.
	resolvedKey := cfg.APIKey

	// Set migrator context for this invocation.
	SetMigratorContext(a.migrator, cfg, resolvedKey)

	// 9. Tool loop: iteratively call the LLM and execute tool calls.
	// The loop continues until either:
	//   a) The LLM returns a response with no tool calls (final answer)
	//   b) The maximum turn count is reached
	// Each LLM call and tool execution is journaled by Restate for durability.
	provider := a.llm.ForConfig(cfg, resolvedKey)
	tools := a.tools.Definitions()

	var toolLog []ToolLogEntry
	var totalUsage AskUsage
	askStart := time.Now()

	// Clear stale progress entries from any previous ask on this session.
	restate.ObjectSend(ctx, ConciergeProgressServiceName, sessionID, "Clear").
		Send(restate.Void{})

	for range cfg.MaxTurns {
		state.TurnCount++

		// Signal "thinking" to the progress tracker so the CLI can show it.
		restate.ObjectSend(ctx, ConciergeProgressServiceName, sessionID, "Update").
			Send(ToolProgressEntry{Name: "thinking", Status: "thinking"})

		// Call LLM inside restate.Run() to make it durable. The LLM response
		// is journaled — on replay, Restate returns the cached result instead
		// of calling the LLM again. This prevents duplicate API calls and costs.
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

		// No tool calls → the LLM produced a final text response for the user.
		if len(llmResp.ToolCalls) == 0 {
			state.Messages = append(state.Messages, Message{
				Role:      "assistant",
				Content:   llmResp.Content,
				Timestamp: now,
			})
			state.LastActiveAt = now
			restate.Set(ctx, "state", state)

			totalUsage.PromptTokens += llmResp.Usage.PromptTokens
			totalUsage.CompletionTokens += llmResp.Usage.CompletionTokens
			totalUsage.TotalTokens += llmResp.Usage.TotalTokens

			return AskResponse{
				Response:   llmResp.Content,
				SessionID:  sessionID,
				TurnCount:  state.TurnCount,
				ToolLog:    toolLog,
				Model:      cfg.Model,
				Provider:   cfg.Provider,
				Usage:      totalUsage,
				DurationMs: time.Since(askStart).Milliseconds(),
			}, nil
		}

		// Accumulate token usage from this LLM turn.
		totalUsage.PromptTokens += llmResp.Usage.PromptTokens
		totalUsage.CompletionTokens += llmResp.Usage.CompletionTokens
		totalUsage.TotalTokens += llmResp.Usage.TotalTokens

		// Append assistant message with tool calls.
		state.Messages = append(state.Messages, Message{
			Role:      "assistant",
			ToolCalls: llmResp.ToolCalls,
			Timestamp: now,
		})

		// Execute each tool call. Read-only tools run immediately; write tools
		// (RequiresApproval=true) suspend on a Restate awakeable for human approval.
		for _, tc := range llmResp.ToolCalls {
			toolStart := time.Now()
			tool := a.tools.Get(tc.Name)
			if tool == nil {
				appendToolError(&state, tc.ID, tc.Name, "unknown tool")
				restate.ObjectSend(ctx, ConciergeProgressServiceName, sessionID, "Update").
					Send(ToolProgressEntry{Name: tc.Name, Status: "error", Error: "unknown tool"})
				toolLog = append(toolLog, ToolLogEntry{Name: tc.Name, Status: "error", Error: "unknown tool", DurationMs: time.Since(toolStart).Milliseconds()})
				continue
			}

			// Signal tool start to the progress tracker.
			restate.ObjectSend(ctx, ConciergeProgressServiceName, sessionID, "Update").
				Send(ToolProgressEntry{Name: tc.Name, Status: "running"})

			if tool.RequiresApproval {
				result, err := a.executeWithApproval(ctx, &state, cfg, tool, tc, now)
				if err != nil {
					appendToolError(&state, tc.ID, tc.Name, err.Error())
					restate.ObjectSend(ctx, ConciergeProgressServiceName, sessionID, "Update").
						Send(ToolProgressEntry{Name: tc.Name, Status: "error", Error: err.Error()})
					toolLog = append(toolLog, ToolLogEntry{Name: tc.Name, Status: "error", Error: err.Error(), DurationMs: time.Since(toolStart).Milliseconds()})
				} else {
					appendToolResult(&state, tc.ID, tc.Name, result)
					restate.ObjectSend(ctx, ConciergeProgressServiceName, sessionID, "Update").
						Send(ToolProgressEntry{Name: tc.Name, Status: "ok"})
					toolLog = append(toolLog, ToolLogEntry{Name: tc.Name, Status: "ok", DurationMs: time.Since(toolStart).Milliseconds()})
				}
			} else {
				result, err := a.executeTool(ctx, tool, tc, state)
				if err != nil {
					appendToolError(&state, tc.ID, tc.Name, err.Error())
					restate.ObjectSend(ctx, ConciergeProgressServiceName, sessionID, "Update").
						Send(ToolProgressEntry{Name: tc.Name, Status: "error", Error: err.Error()})
					toolLog = append(toolLog, ToolLogEntry{Name: tc.Name, Status: "error", Error: err.Error(), DurationMs: time.Since(toolStart).Milliseconds()})
				} else {
					appendToolResult(&state, tc.ID, tc.Name, result)
					restate.ObjectSend(ctx, ConciergeProgressServiceName, sessionID, "Update").
						Send(ToolProgressEntry{Name: tc.Name, Status: "ok"})
					toolLog = append(toolLog, ToolLogEntry{Name: tc.Name, Status: "ok", DurationMs: time.Since(toolStart).Milliseconds()})
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
		Response:   turnLimitMsg,
		SessionID:  sessionID,
		TurnCount:  state.TurnCount,
		ToolLog:    toolLog,
		Model:      cfg.Model,
		Provider:   cfg.Provider,
		Usage:      totalUsage,
		DurationMs: time.Since(askStart).Milliseconds(),
	}, nil
}

// executeTool runs a tool and returns its result. Tools receive the Restate
// context (for making durable cross-service calls) and the current session state
// (for accessing account/workspace context).
func (a *ConciergeSession) executeTool(ctx restate.Context, tool *ToolDef, tc ToolCall, state SessionState) (string, error) {
	return tool.Execute(ctx, tc.Args, state)
}

// executeWithApproval implements the human-in-the-loop approval flow for write tools.
//
// Flow:
//  1. Create a Restate awakeable — a durable promise that can be resolved externally
//  2. Start a timeout timer (ApprovalTTL, default 5 minutes)
//  3. Store the awakeable ID in SessionState so transports can discover it via GetStatus()
//  4. Race the awakeable against the timer using restate.WaitFirst()
//     5a. If timer wins → auto-reject (timeout)
//     5b. If awakeable wins → check the decision, execute tool if approved
//
// The awakeable is resolved externally by ApprovalRelay.Resolve(), which is called
// by the CLI (polling GetStatus) or Slack gateway (interactive button callback).
// The entire handler is suspended during the wait — no compute resources are consumed.
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

// GetHistory returns the conversation history. This is a shared handler
// (ObjectSharedContext) meaning it can run concurrently with other shared handlers
// and does NOT block exclusive handlers like Ask(). Safe for polling.
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

// GetStatus returns session metadata. This is a shared handler used by transports
// to poll for pending approvals. When PendingApproval is non-nil, the transport
// should display the approval prompt and allow the user to approve or reject.
func (a *ConciergeSession) GetStatus(ctx restate.ObjectSharedContext) (SessionStatus, error) {
	statePtr, err := restate.Get[*SessionState](ctx, "state")
	if err != nil {
		return SessionStatus{}, err
	}
	if statePtr == nil {
		// No session yet — fall back to global config so the user can see
		// the configured provider/model even before the first ask.
		cfg, err := restate.Object[ConciergeConfiguration](
			ctx, ConciergeConfigServiceName, "global", "Get",
		).Request(restate.Void{})
		if err != nil {
			return SessionStatus{}, nil
		}
		return SessionStatus{
			Provider: cfg.Provider,
			Model:    cfg.Model,
		}, nil
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

// Reset clears conversation history and state. This is an exclusive handler
// (ObjectContext) that wipes all Restate KV state for this session key.
func (a *ConciergeSession) Reset(ctx restate.ObjectContext) error {
	restate.ClearAll(ctx)
	return nil
}

// Expire proactively cleans up expired sessions. Called via a delayed self-message
// scheduled when the session is first created. If the session has been extended
// (e.g., by further activity), Expire re-schedules itself for the new expiry time.
// This pattern avoids relying on external cron jobs for session cleanup.
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

// appendToolResult adds a successful tool result message to the conversation history.
// The ToolCallID correlates this result back to the LLM's original tool call request.
func appendToolResult(state *SessionState, toolCallID, toolName, result string) {
	state.Messages = append(state.Messages, Message{
		Role:       "tool",
		Content:    result,
		ToolCallID: toolCallID,
		Name:       toolName,
	})
}

// appendToolError adds a tool error message to the conversation history.
// The error is prefixed with "Error: " so the LLM can distinguish failures
// from successful results and respond appropriately to the user.
func appendToolError(state *SessionState, toolCallID, toolName, errMsg string) {
	state.Messages = append(state.Messages, Message{
		Role:       "tool",
		Content:    fmt.Sprintf("Error: %s", errMsg),
		ToolCallID: toolCallID,
		Name:       toolName,
	})
}
