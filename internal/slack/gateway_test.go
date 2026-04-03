package slack

import (
	"context"
	"testing"
)

func TestShouldHandle_DM(t *testing.T) {
	msg := &parsedMessage{
		UserID:      "U001",
		ChannelType: "im",
	}
	if !shouldHandle(context.Background(), msg, "UBOT", nil) {
		t.Error("should handle DM from non-bot user")
	}
}

func TestShouldHandle_BotMessage(t *testing.T) {
	msg := &parsedMessage{
		UserID:      "UBOT",
		ChannelType: "im",
	}
	if shouldHandle(context.Background(), msg, "UBOT", nil) {
		t.Error("should not handle messages from the bot itself")
	}
}

func TestShouldHandle_SubType(t *testing.T) {
	msg := &parsedMessage{
		UserID:      "U001",
		ChannelType: "im",
		SubType:     "channel_join",
	}
	if shouldHandle(context.Background(), msg, "UBOT", nil) {
		t.Error("should not handle messages with subtypes")
	}
}

func TestShouldHandle_ChannelNotThread(t *testing.T) {
	msg := &parsedMessage{
		UserID:      "U001",
		ChannelType: "channel",
	}
	if shouldHandle(context.Background(), msg, "UBOT", nil) {
		t.Error("should not handle channel messages without thread")
	}
}

func TestDeriveSessionKey_DM(t *testing.T) {
	msg := &parsedMessage{
		UserID:      "U001",
		TeamID:      "T123",
		ChannelType: "im",
	}
	key := deriveSessionKeyFromMessage(msg, nil, context.Background())
	expected := "slack:T123:U001"
	if key != expected {
		t.Errorf("expected %q, got %q", expected, key)
	}
}

func TestDeriveSessionKey_NonDM_NoThread(t *testing.T) {
	msg := &parsedMessage{
		UserID:      "U001",
		TeamID:      "T123",
		ChannelType: "channel",
	}
	key := deriveSessionKeyFromMessage(msg, nil, context.Background())
	if key != "" {
		t.Errorf("expected empty key for non-DM without thread, got %q", key)
	}
}

func TestRedacted(t *testing.T) {
	cfg := SlackGatewayConfiguration{
		BotToken: "xoxb-secret",
		AppToken: "xapp-secret",
	}
	redacted := cfg.Redacted()
	if redacted.BotToken != "***" {
		t.Errorf("expected '***', got %q", redacted.BotToken)
	}
	if redacted.AppToken != "***" {
		t.Errorf("expected '***', got %q", redacted.AppToken)
	}
	// Original should not be modified
	if cfg.BotToken != "xoxb-secret" {
		t.Errorf("original was mutated: %q", cfg.BotToken)
	}
}

func TestRedacted_EmptyTokens(t *testing.T) {
	cfg := SlackGatewayConfiguration{
		BotTokenRef: "ssm:///praxis/slack/bot-token",
	}
	redacted := cfg.Redacted()
	if redacted.BotToken != "" {
		t.Errorf("expected empty bot token for ref-only, got %q", redacted.BotToken)
	}
	if redacted.BotTokenRef != "ssm:///praxis/slack/bot-token" {
		t.Errorf("ref should pass through: %q", redacted.BotTokenRef)
	}
}
