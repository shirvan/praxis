package slack

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	slackpkg "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// Gateway manages the Slack Socket Mode connection and message routing.
type Gateway struct {
	client        *socketmode.Client
	restateClient *ingress.Client
	threads       *ThreadTracker
	configVersion int
}

// NewGateway creates a new Slack gateway.
func NewGateway(rc *ingress.Client) *Gateway {
	return &Gateway{
		restateClient: rc,
		threads:       NewThreadTracker(rc),
	}
}

// connect fetches the current SlackGatewayConfig and (re)creates the Socket Mode client.
func (g *Gateway) connect(ctx context.Context) error {
	cfg, err := fetchConfig(ctx, g.restateClient)
	if err != nil {
		return fmt.Errorf("fetch slack config: %w", err)
	}
	g.configVersion = cfg.Version

	botToken, err := resolveToken(cfg.BotToken, cfg.BotTokenRef)
	if err != nil {
		return fmt.Errorf("resolve bot token: %w", err)
	}
	appToken, err := resolveToken(cfg.AppToken, cfg.AppTokenRef)
	if err != nil {
		return fmt.Errorf("resolve app token: %w", err)
	}

	api := slackpkg.New(botToken, slackpkg.OptionAppLevelToken(appToken))
	g.client = socketmode.New(api)
	return nil
}

// Run manages the Socket Mode lifecycle with automatic reconnection on config changes.
func (g *Gateway) Run(ctx context.Context) error {
	for {
		if err := g.connect(ctx); err != nil {
			return err
		}

		connCtx, connCancel := context.WithCancel(ctx)
		go g.handleEvents(connCtx)
		go g.watchConfigVersion(connCtx, connCancel)

		err := g.client.RunContext(connCtx)
		connCancel()

		if ctx.Err() != nil {
			return nil
		}

		slog.Info("socket mode disconnected, reconnecting...", "err", err)
	}
}

// watchConfigVersion polls config every 30s and triggers reconnect on version change.
func (g *Gateway) watchConfigVersion(ctx context.Context, connCancel context.CancelFunc) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cfg, err := fetchConfig(ctx, g.restateClient)
			if err != nil {
				slog.Warn("config version check failed", "err", err)
				continue
			}
			if cfg.Version != g.configVersion {
				slog.Info("config version changed, triggering reconnect",
					"old", g.configVersion, "new", cfg.Version)
				connCancel()
				return
			}
		}
	}
}

func (g *Gateway) handleEvents(ctx context.Context) {
	for evt := range g.client.Events {
		switch evt.Type {
		case socketmode.EventTypeEventsAPI:
			g.client.Ack(*evt.Request)
			g.handleMessage(ctx, evt)
		case socketmode.EventTypeInteractive:
			g.client.Ack(*evt.Request)
			g.handleInteractionEvent(ctx, evt)
		}
	}
}

// handleMessage fetches config dynamically for each message.
func (g *Gateway) handleMessage(ctx context.Context, evt socketmode.Event) {
	msg, ok := parseMessageEvent(evt)
	if !ok {
		return
	}

	cfg, err := fetchConfig(ctx, g.restateClient)
	if err != nil {
		slog.Error("fetch config for message", "err", err)
		return
	}

	if !shouldHandle(ctx, msg, cfg.BotUserID, g.threads) {
		return
	}

	if !isUserAllowed(msg.UserID, cfg.AllowedUsers) {
		g.postEphemeral(msg.Channel, msg.UserID, notAllowedMessage)
		return
	}

	sessionKey := deriveSessionKeyFromMessage(msg, g.threads, ctx)
	if sessionKey == "" {
		return
	}

	idempotencyKey := fmt.Sprintf("%s:%s", msg.Channel, msg.TimeStamp)

	sendResp, err := ingress.ObjectSend[AskRequest](
		g.restateClient, "ConciergeSession", sessionKey, "Ask",
	).Send(context.Background(), AskRequest{
		Prompt:    msg.Text,
		Workspace: cfg.Workspace,
		Source:    "slack:dm",
	}, restate.WithIdempotencyKey(idempotencyKey))
	if err != nil {
		slog.Error("failed to send ask", "session", sessionKey, "err", err)
		return
	}
	invocationID := sendResp.Id()

	g.postTypingIndicator(msg.Channel)

	pollCtx, pollCancel := context.WithCancel(ctx)
	go func() {
		backoff := 200 * time.Millisecond
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-time.After(backoff):
			}

			status, err := ingress.Object[restate.Void, SessionStatus](
				g.restateClient, "ConciergeSession", sessionKey, "GetStatus",
			).Request(pollCtx, restate.Void{})
			if err != nil || pollCtx.Err() != nil {
				return
			}

			if status.PendingApproval != nil {
				g.postApprovalPrompt(msg.Channel, status.PendingApproval)
				return
			}

			if backoff < 2*time.Second {
				backoff = time.Duration(float64(backoff) * 1.5)
			}
		}
	}()

	go func() {
		resp, err := ingress.AttachInvocation[AskResponse](
			g.restateClient, invocationID,
		).Attach(ctx)
		pollCancel()

		if err != nil {
			slog.Error("ask failed", "session", sessionKey, "err", err)
			g.postMessage(msg.Channel, "_I encountered an error processing your request._")
			return
		}

		blocks := formatResponse(resp)
		g.postBlocks(msg.Channel, blocks)
	}()
}

// handleInteractionEvent processes a Slack interaction callback.
func (g *Gateway) handleInteractionEvent(ctx context.Context, evt socketmode.Event) {
	callback, ok := evt.Data.(slackpkg.InteractionCallback)
	if !ok {
		return
	}
	if err := g.handleInteraction(callback); err != nil {
		slog.Error("interaction handling failed", "err", err)
	}
}

// handleInteraction processes button clicks for approvals.
func (g *Gateway) handleInteraction(callback slackpkg.InteractionCallback) error {
	cfg, err := fetchConfig(context.Background(), g.restateClient)
	if err != nil {
		return fmt.Errorf("fetch config for interaction: %w", err)
	}

	if !isUserAllowed(callback.User.ID, cfg.AllowedUsers) {
		g.postEphemeral(callback.Channel.ID, callback.User.ID, notAllowedMessage)
		return nil
	}

	for _, action := range callback.ActionCallback.BlockActions {
		awakeableID := action.Value

		switch action.ActionID {
		case "approve":
			return g.approveAction(awakeableID, true, "", callback.User.ID)
		case "reject":
			return g.approveAction(awakeableID, false, "Rejected via Slack", callback.User.ID)
		}
	}
	return nil
}

// approveAction calls the ApprovalRelay via Restate ingress.
func (g *Gateway) approveAction(awakeableID string, approved bool, reason string, actor string) error {
	_, err := ingress.Service[ApprovalRelayRequest, restate.Void](
		g.restateClient, "ApprovalRelay", "Resolve",
	).Request(context.Background(), ApprovalRelayRequest{
		AwakeableID: awakeableID,
		Approved:    approved,
		Reason:      reason,
		Actor:       actor,
	})
	return err
}

// fetchConfig loads the current SlackGatewayConfig from Restate.
func fetchConfig(ctx context.Context, rc *ingress.Client) (SlackGatewayConfiguration, error) {
	return ingress.Object[restate.Void, SlackGatewayConfiguration](
		rc, SlackGatewayConfigServiceName, "global", "Get",
	).Request(ctx, restate.Void{})
}

// shouldHandle returns true if this message event should be processed.
func shouldHandle(ctx context.Context, msg *parsedMessage, botUserID string, threads *ThreadTracker) bool {
	if msg.UserID == botUserID {
		return false
	}
	if msg.SubType != "" {
		return false
	}
	if msg.ChannelType == "im" {
		return true
	}
	if msg.ThreadTimeStamp != "" && threads.IsWatchThread(ctx, msg.Channel, msg.ThreadTimeStamp) {
		return true
	}
	return false
}

// deriveSessionKeyFromMessage returns a stable session key for the message.
func deriveSessionKeyFromMessage(msg *parsedMessage, threads *ThreadTracker, ctx context.Context) string {
	if msg.ChannelType == "im" {
		return fmt.Sprintf("slack:%s:%s", msg.TeamID, msg.UserID)
	}
	if msg.ThreadTimeStamp != "" && threads.IsWatchThread(ctx, msg.Channel, msg.ThreadTimeStamp) {
		return fmt.Sprintf("slack:thread:%s:%s", msg.Channel, msg.ThreadTimeStamp)
	}
	return ""
}

// parsedMessage holds the fields extracted from a Slack message event.
type parsedMessage struct {
	UserID          string
	Channel         string
	ChannelType     string
	Text            string
	TimeStamp       string
	ThreadTimeStamp string
	TeamID          string
	SubType         string
}

// parseMessageEvent extracts a parsedMessage from a socketmode event.
func parseMessageEvent(evt socketmode.Event) (*parsedMessage, bool) {
	evtAPI, ok := evt.Data.(slackevents.EventsAPIEvent)
	if !ok {
		return nil, false
	}
	inner, ok := evtAPI.InnerEvent.Data.(*slackevents.MessageEvent)
	if !ok {
		return nil, false
	}
	return &parsedMessage{
		UserID:          inner.User,
		Channel:         inner.Channel,
		ChannelType:     inner.ChannelType,
		Text:            inner.Text,
		TimeStamp:       inner.TimeStamp,
		ThreadTimeStamp: inner.ThreadTimeStamp,
		TeamID:          evtAPI.TeamID,
		SubType:         inner.SubType,
	}, true
}

// postMessage posts a simple text message.
func (g *Gateway) postMessage(channel, text string) {
	cfg, err := fetchConfig(context.Background(), g.restateClient)
	if err != nil {
		slog.Error("postMessage: fetch config", "err", err)
		return
	}
	botToken, err := resolveToken(cfg.BotToken, cfg.BotTokenRef)
	if err != nil {
		slog.Error("postMessage: resolve token", "err", err)
		return
	}
	api := slackpkg.New(botToken)
	_, _, err = api.PostMessage(channel, slackpkg.MsgOptionText(text, false))
	if err != nil {
		slog.Error("postMessage failed", "channel", channel, "err", err)
	}
}

// postBlocks posts Block Kit blocks.
func (g *Gateway) postBlocks(channel string, blocks []slackpkg.Block) {
	cfg, err := fetchConfig(context.Background(), g.restateClient)
	if err != nil {
		slog.Error("postBlocks: fetch config", "err", err)
		return
	}
	botToken, err := resolveToken(cfg.BotToken, cfg.BotTokenRef)
	if err != nil {
		slog.Error("postBlocks: resolve token", "err", err)
		return
	}
	api := slackpkg.New(botToken)
	_, _, err = api.PostMessage(channel, slackpkg.MsgOptionBlocks(blocks...))
	if err != nil {
		slog.Error("postBlocks failed", "channel", channel, "err", err)
	}
}

// postEphemeral posts an ephemeral message visible only to the given user.
func (g *Gateway) postEphemeral(channel, userID, text string) {
	cfg, err := fetchConfig(context.Background(), g.restateClient)
	if err != nil {
		slog.Error("postEphemeral: fetch config", "err", err)
		return
	}
	botToken, err := resolveToken(cfg.BotToken, cfg.BotTokenRef)
	if err != nil {
		slog.Error("postEphemeral: resolve token", "err", err)
		return
	}
	api := slackpkg.New(botToken)
	_, err = api.PostEphemeral(channel, userID, slackpkg.MsgOptionText(text, false))
	if err != nil {
		slog.Error("postEphemeral failed", "channel", channel, "err", err)
	}
}

// postApprovalPrompt posts approval buttons.
func (g *Gateway) postApprovalPrompt(channel string, approval *ApprovalInfo) {
	blocks := formatApproval(approval)
	g.postBlocks(channel, blocks)
}

// postTypingIndicator sends a typing indicator to the channel.
// Note: Slack Bot tokens cannot send typing indicators via the API,
// so this is a no-op placeholder for future enhancement.
func (g *Gateway) postTypingIndicator(_ string) {}
