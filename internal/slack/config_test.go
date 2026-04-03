package slack

import (
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"
)

func TestSlackGatewayConfig_ConfigureAndGet(t *testing.T) {
	env := restatetest.Start(t,
		restate.Reflect(SlackGatewayConfig{}),
	)
	client := env.Ingress()

	// Configure
	_, err := ingress.Object[SlackConfigRequest, restate.Void](
		client, SlackGatewayConfigServiceName, SlackGatewayConfigGlobalKey, "Configure",
	).Request(t.Context(), SlackConfigRequest{
		BotToken:     "xoxb-test-token",
		AppToken:     "xapp-test-token",
		EventChannel: "#alerts",
		AllowedUsers: []string{"U001", "U002"},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	// Get should return redacted tokens
	cfg, err := ingress.Object[restate.Void, SlackGatewayConfiguration](
		client, SlackGatewayConfigServiceName, SlackGatewayConfigGlobalKey, "Get",
	).Request(t.Context(), restate.Void{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if cfg.BotToken != "***" {
		t.Errorf("expected redacted bot token '***', got %q", cfg.BotToken)
	}
	if cfg.AppToken != "***" {
		t.Errorf("expected redacted app token '***', got %q", cfg.AppToken)
	}
	if cfg.EventChannel != "#alerts" {
		t.Errorf("expected event channel '#alerts', got %q", cfg.EventChannel)
	}
	if cfg.Version != 1 {
		t.Errorf("expected version 1, got %d", cfg.Version)
	}
	if len(cfg.AllowedUsers) != 2 {
		t.Errorf("expected 2 allowed users, got %d", len(cfg.AllowedUsers))
	}
}

func TestSlackGatewayConfig_PartialUpdate(t *testing.T) {
	env := restatetest.Start(t,
		restate.Reflect(SlackGatewayConfig{}),
	)
	client := env.Ingress()

	// Initial configure
	_, err := ingress.Object[SlackConfigRequest, restate.Void](
		client, SlackGatewayConfigServiceName, SlackGatewayConfigGlobalKey, "Configure",
	).Request(t.Context(), SlackConfigRequest{
		BotToken:     "xoxb-original",
		EventChannel: "#original",
	})
	if err != nil {
		t.Fatalf("Configure 1: %v", err)
	}

	// Partial update — only change event channel
	_, err = ingress.Object[SlackConfigRequest, restate.Void](
		client, SlackGatewayConfigServiceName, SlackGatewayConfigGlobalKey, "Configure",
	).Request(t.Context(), SlackConfigRequest{
		EventChannel: "#updated",
	})
	if err != nil {
		t.Fatalf("Configure 2: %v", err)
	}

	cfg, err := ingress.Object[restate.Void, SlackGatewayConfiguration](
		client, SlackGatewayConfigServiceName, SlackGatewayConfigGlobalKey, "Get",
	).Request(t.Context(), restate.Void{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Bot token should still be set (redacted)
	if cfg.BotToken != "***" {
		t.Errorf("expected bot token to persist, got %q", cfg.BotToken)
	}
	if cfg.EventChannel != "#updated" {
		t.Errorf("expected event channel '#updated', got %q", cfg.EventChannel)
	}
	if cfg.Version != 2 {
		t.Errorf("expected version 2, got %d", cfg.Version)
	}
}

func TestSlackGatewayConfig_AllowedUsers(t *testing.T) {
	env := restatetest.Start(t,
		restate.Reflect(SlackGatewayConfig{}),
	)
	client := env.Ingress()

	// Must configure first — allowed-users handlers require existing config
	_, err := ingress.Object[SlackConfigRequest, restate.Void](
		client, SlackGatewayConfigServiceName, SlackGatewayConfigGlobalKey, "Configure",
	).Request(t.Context(), SlackConfigRequest{
		BotToken: "xoxb-test",
		AppToken: "xapp-test",
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	// Set allowed users
	_, err = ingress.Object[SetAllowedUsersRequest, restate.Void](
		client, SlackGatewayConfigServiceName, SlackGatewayConfigGlobalKey, "SetAllowedUsers",
	).Request(t.Context(), SetAllowedUsersRequest{
		UserIDs: []string{"U001", "U002", "U003"},
	})
	if err != nil {
		t.Fatalf("SetAllowedUsers: %v", err)
	}

	// Add another
	_, err = ingress.Object[string, restate.Void](
		client, SlackGatewayConfigServiceName, SlackGatewayConfigGlobalKey, "AddAllowedUser",
	).Request(t.Context(), "U004")
	if err != nil {
		t.Fatalf("AddAllowedUser: %v", err)
	}

	// Add duplicate — should not appear twice
	_, err = ingress.Object[string, restate.Void](
		client, SlackGatewayConfigServiceName, SlackGatewayConfigGlobalKey, "AddAllowedUser",
	).Request(t.Context(), "U001")
	if err != nil {
		t.Fatalf("AddAllowedUser duplicate: %v", err)
	}

	// Remove one
	_, err = ingress.Object[string, restate.Void](
		client, SlackGatewayConfigServiceName, SlackGatewayConfigGlobalKey, "RemoveAllowedUser",
	).Request(t.Context(), "U002")
	if err != nil {
		t.Fatalf("RemoveAllowedUser: %v", err)
	}

	cfg, err := ingress.Object[restate.Void, SlackGatewayConfiguration](
		client, SlackGatewayConfigServiceName, SlackGatewayConfigGlobalKey, "Get",
	).Request(t.Context(), restate.Void{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	expected := map[string]bool{"U001": true, "U003": true, "U004": true}
	if len(cfg.AllowedUsers) != len(expected) {
		t.Fatalf("expected %d allowed users, got %d: %v", len(expected), len(cfg.AllowedUsers), cfg.AllowedUsers)
	}
	for _, u := range cfg.AllowedUsers {
		if !expected[u] {
			t.Errorf("unexpected user %s in allowed list", u)
		}
	}
}
