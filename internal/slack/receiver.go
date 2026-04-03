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
// from the SinkRouter and triggers thread creation.
type SlackEventReceiver struct{}

func (SlackEventReceiver) ServiceName() string { return SlackEventReceiverServiceName }

// Receive is called by the SinkRouter when a matching event fires.
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
