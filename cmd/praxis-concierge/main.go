package main

import (
	"context"
	"log/slog"
	"os"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"

	"github.com/shirvan/praxis/internal/concierge"
	"github.com/shirvan/praxis/internal/core/config"
)

func main() {
	addr := os.Getenv("PRAXIS_LISTEN_ADDR")
	if addr == "" {
		addr = "0.0.0.0:9080"
	}
	rp := config.DefaultRetryPolicy()

	srv := server.NewRestate().
		Bind(restate.Reflect(concierge.NewConciergeSession(), rp)).
		Bind(restate.Reflect(concierge.ConciergeConfig{}, rp)).
		Bind(restate.Reflect(concierge.ApprovalRelay{}, rp)).
		Bind(restate.Reflect(concierge.ConciergeProgress{}, rp))

	slog.Info("starting Praxis concierge runtime", "addr", addr) //nolint:gosec // G706 addr is from env var, not user input
	if err := srv.Start(context.Background(), addr); err != nil {
		slog.Error("praxis-concierge exited", "err", err.Error()) //nolint:gosec // G706 error message is safe
		os.Exit(1)
	}
}
