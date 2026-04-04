package main

import (
	"context"
	"log/slog"
	"os"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"

	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/core/orchestrator"
)

// praxis-notifications hosts the CloudEvents event bus, per-deployment event
// store, cross-deployment index, and notification sink router.
func main() {
	cfg := config.Load()
	rp := config.DefaultRetryPolicy()

	srv := server.NewRestate().
		Bind(restate.Reflect(orchestrator.NewEventBus(cfg.SchemaDir), rp)).
		Bind(restate.Reflect(orchestrator.DeploymentEventStore{}, rp)).
		Bind(restate.Reflect(orchestrator.EventIndex{}, rp)).
		Bind(restate.Reflect(orchestrator.ResourceEventOwnerObj{}, rp)).
		Bind(restate.Reflect(orchestrator.ResourceEventBridge{}, rp)).
		Bind(restate.Reflect(orchestrator.SinkRouter{}, rp)).
		Bind(restate.Reflect(orchestrator.NewNotificationSinkConfig(cfg.SchemaDir), rp))

	slog.Info("starting Praxis notifications runtime", "addr", cfg.ListenAddr)
	if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
		slog.Error("Praxis notifications exited", "err", err.Error())
		os.Exit(1)
	}
}
