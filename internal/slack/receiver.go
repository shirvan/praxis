package slack

import (
	_ "embed"
	"fmt"
	"log/slog"

	restate "github.com/restatedev/sdk-go"
)

//go:embed prompts/event_analysis.txt
var eventAnalysisPromptTemplate string

// SlackEventReceiver is a stateless Restate service that receives CloudEvents
// from the SinkRouter and triggers thread creation. Being stateless (restate.Context
// rather than ObjectContext), multiple events can be processed concurrently.
//
// Event flow:
//
//	SinkRouter → Receive() → match watch rules → create Slack thread
//	  → record thread state (for dedup) → fire AnalyzeAndReply (async)
//	    → ask ConciergeSession → post analysis as thread reply
type SlackEventReceiver struct{}

func (SlackEventReceiver) ServiceName() string { return SlackEventReceiverServiceName }

// Receive is called by the SinkRouter when a matching event fires.
// For each matching watch rule, it:
//  1. Checks for an existing thread (dedup via SlackThreadState)
//  2. Creates a new Slack thread with the event summary
//  3. Records the thread state for future dedup and reverse lookup
//  4. Fires an async AnalyzeAndReply to get concierge analysis
func (s SlackEventReceiver) Receive(ctx restate.Context, event CloudEventEnvelope) error {
	config, err := restate.Object[SlackGatewayConfiguration](
		ctx, SlackGatewayConfigServiceName, "global", "Get",
	).Request(restate.Void{})
	if err != nil {
		return err
	}

	botToken, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return resolveToken(config.BotToken, config.BotTokenRef)
	})
	if err != nil {
		return err
	}

	watches, err := restate.Object[[]WatchRule](
		ctx, SlackWatchConfigServiceName, "global", "ListWatches",
	).Request(restate.Void{})
	if err != nil {
		return err
	}

	matched := matchAllRules(watches, event)
	if len(matched) == 0 {
		return nil
	}

	for _, rule := range matched {
		channel := rule.Channel
		if channel == "" {
			channel = config.EventChannel
		}

		// Deduplicate: check if a thread already exists for this event+rule
		// combination. The dedupeKey combines the event ID and rule ID so the
		// same event can create threads for different rules, but not duplicate
		// threads for the same rule.
		dedupeKey := fmt.Sprintf("thread:%s:%s", event.ID, rule.ID)

		existing, err := restate.Object[*string](
			ctx, SlackThreadStateServiceName, dedupeKey, "GetThreadTS",
		).Request(restate.Void{})
		if err != nil {
			return err
		}
		if existing != nil {
			continue
		}

		threadTS, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return postEventThread(botToken, channel, event)
		})
		if err != nil {
			return err
		}

		sessionKey := fmt.Sprintf("slack:thread:%s:%s", channel, threadTS)
		restate.ObjectSend(ctx, SlackThreadStateServiceName, dedupeKey, "RecordThread").
			Send(ThreadRecord{
				ChannelID:   channel,
				ThreadTS:    threadTS,
				SessionKey:  sessionKey,
				WatchRuleID: rule.ID,
				EventID:     event.ID,
				EventType:   event.Type,
			})

		prompt := buildEventAnalysisPrompt(event)
		restate.ServiceSend(ctx, SlackEventReceiverServiceName, "AnalyzeAndReply").
			Send(AnalyzeAndReplyRequest{
				SessionKey: sessionKey,
				Prompt:     prompt,
				Workspace:  event.Extensions["workspace"],
				ChannelID:  channel,
				ThreadTS:   threadTS,
			})
	}

	return nil
}

// AnalyzeAndReply sends the analysis to the concierge and posts the reply as a thread.
// This runs as a separate Restate invocation (sent via ServiceSend from Receive)
// so the thread creation is not blocked by the potentially slow LLM analysis.
// If analysis fails, a fallback message is posted inviting the user to ask manually.
func (s SlackEventReceiver) AnalyzeAndReply(ctx restate.Context, req AnalyzeAndReplyRequest) error {
	config, err := restate.Object[SlackGatewayConfiguration](
		ctx, SlackGatewayConfigServiceName, "global", "Get",
	).Request(restate.Void{})
	if err != nil {
		return err
	}
	botToken, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return resolveToken(config.BotToken, config.BotTokenRef)
	})
	if err != nil {
		return err
	}

	// Send the ask as a one-way message and immediately attach to its result.
	// This pattern (Send + GetInvocationId + AttachInvocation) lets us get a
	// durable handle to the concierge invocation that survives receiver restarts.
	invocationID := restate.ObjectSend(ctx, "ConciergeSession", req.SessionKey, "Ask").
		Send(AskRequest{
			Prompt:    req.Prompt,
			Workspace: req.Workspace,
			Source:    "slack:thread",
		}).GetInvocationId()

	resp, err := restate.AttachInvocation[AskResponse](ctx, invocationID).Response()
	if err != nil {
		slog.Error("concierge analysis failed", "session", req.SessionKey, "err", err)
		_, _ = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, postThreadReply(botToken, req.ChannelID, req.ThreadTS,
				"_I encountered an error while analyzing this event. Reply in this thread to ask me about it._")
		})
		return nil
	}

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, postThreadReply(botToken, req.ChannelID, req.ThreadTS, resp.Response)
	})
	return err
}

// buildEventAnalysisPrompt constructs a focused prompt for the concierge.
func buildEventAnalysisPrompt(event CloudEventEnvelope) string {
	return fmt.Sprintf(eventAnalysisPromptTemplate,
		event.Type,
		event.Extensions["severity"],
		event.Extensions["deployment"],
		event.Extensions["workspace"],
		event.Subject,
		event.Time,
		string(event.DataJSON),
	)
}

// resolveToken returns the literal token if set, otherwise returns the ref for
// resolution. In production, refs point to SSM parameters.
//
// Token priority: literal > ref. If a literal token is "***" (the redacted
// sentinel from Get), it is treated as unset. This prevents accidentally
// using a redacted value as an actual token.
func resolveToken(literal, ref string) (string, error) {
	if literal != "" && literal != "***" {
		return literal, nil
	}
	if ref != "" {
		// In a full implementation, this would call ssmresolver.Resolve.
		// For now, return the ref as-is since SSM resolution requires AWS credentials.
		return "", fmt.Errorf("SSM token resolution not yet wired (ref: %s)", ref)
	}
	return "", fmt.Errorf("no token configured (provide a literal token or SSM ref)")
}
