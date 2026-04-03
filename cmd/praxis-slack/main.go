package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/restatedev/sdk-go/server"

	"github.com/shirvan/praxis/internal/slack"
)

func main() {
	addr := os.Getenv("PRAXIS_LISTEN_ADDR")
	if addr == "" {
		addr = "0.0.0.0:9080"
	}
	restateURI := os.Getenv("RESTATE_URI")
	if restateURI == "" {
		restateURI = "http://localhost:8080"
	}

	// 1. Register Restate services
	srv := server.NewRestate().
		Bind(restate.Reflect(slack.SlackGatewayConfig{})).
		Bind(restate.Reflect(slack.SlackWatchConfig{})).
		Bind(restate.Reflect(slack.SlackThreadState{})).
		Bind(restate.Reflect(slack.SlackEventReceiver{}))

	// 2. Start Restate server in background
	go func() {
		slog.Info("starting praxis-slack restate runtime", "addr", addr)
		if err := srv.Start(context.Background(), addr); err != nil {
			slog.Error("restate server exited", "err", err.Error())
			os.Exit(1)
		}
	}()

	// 3. Create gateway with Restate client
	rc := ingress.NewClient(restateURI)

	// Verify Slack config exists
	if _, err := loadSlackConfig(rc); err != nil {
		slog.Error("slack gateway not configured", "err", err.Error())
		slog.Info("configure with: praxis concierge slack configure --bot-token xoxb-... --app-token xapp-...")
		os.Exit(1)
	}

	// 3a. Startup sink re-sync
	if err := syncSinkOnStartup(rc); err != nil {
		slog.Warn("failed to sync notification sink on startup", "err", err)
	}

	// 4. Start Socket Mode connection to Slack
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	gateway := slack.NewGateway(rc)

	// 5. Clean shutdown: deregister the notification sink
	go func() {
		<-ctx.Done()
		slog.Info("shutting down — removing notification sink")
		if err := removeSink(rc, "slack-gateway"); err != nil {
			slog.Warn("failed to remove sink on shutdown (operator can run: "+
				"praxis notifications sink remove slack-gateway)", "err", err.Error())
		}
	}()

	if err := gateway.Run(ctx); err != nil && ctx.Err() == nil {
		slog.Error("slack gateway exited", "err", err.Error())
		os.Exit(1)
	}
}

func loadSlackConfig(rc *ingress.Client) (*slack.SlackGatewayConfiguration, error) {
	cfg, err := ingress.Object[restate.Void, slack.SlackGatewayConfiguration](
		rc, slack.SlackGatewayConfigServiceName, slack.SlackGatewayConfigGlobalKey, "Get",
	).Request(context.Background(), restate.Void{})
	if err != nil {
		return nil, err
	}
	if cfg.BotToken == "" && cfg.BotTokenRef == "" {
		return nil, fmt.Errorf("no bot token configured")
	}
	return &cfg, nil
}

func syncSinkOnStartup(rc *ingress.Client) error {
	ctx := context.Background()

	watches, err := ingress.Object[restate.Void, []slack.WatchRule](
		rc, slack.SlackWatchConfigServiceName, slack.SlackWatchConfigGlobalKey, "ListWatches",
	).Request(ctx, restate.Void{})
	if err != nil {
		return fmt.Errorf("list watches: %w", err)
	}

	config, err := ingress.Object[restate.Void, slack.SlackGatewayConfiguration](
		rc, slack.SlackGatewayConfigServiceName, slack.SlackGatewayConfigGlobalKey, "Get",
	).Request(ctx, restate.Void{})
	if err != nil {
		return fmt.Errorf("get config: %w", err)
	}

	if len(watches) == 0 || slack.AllDisabled(watches) {
		_ = removeSink(rc, "slack-gateway")
		return nil
	}

	merged := slack.MergeFilters(watches)
	_, err = ingress.Object[slack.SinkRegistration, restate.Void](
		rc, "NotificationSinkConfig", "global", "Upsert",
	).Request(ctx, slack.SinkRegistration{
		Name:    "slack-gateway",
		Type:    "restate_rpc",
		Target:  slack.SlackEventReceiverServiceName,
		Handler: "Receive",
		Filter:  merged,
	})
	if err != nil {
		return fmt.Errorf("register sink: %w", err)
	}

	slog.Info("notification sink synced on startup",
		"rules", len(watches), "channel", config.EventChannel)
	return nil
}

func removeSink(rc *ingress.Client, name string) error {
	_, err := ingress.Object[string, restate.Void](
		rc, "NotificationSinkConfig", "global", "Remove",
	).Request(context.Background(), name)
	return err
}
