package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/restatedev/sdk-go/server"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/config"
	identitypack "github.com/shirvan/praxis/internal/driverpack/identity"
)

func main() {
	cfg := config.Load()
	auth := authservice.NewAuthClient()

	srv := server.NewRestate()
	for _, definition := range identitypack.Definitions(auth) {
		srv.Bind(definition)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	slog.Info("starting identity driver pack", "addr", cfg.ListenAddr)
	err := srv.Start(ctx, cfg.ListenAddr)
	stop()
	if err != nil {
		slog.Error("identity driver pack exited", "err", err.Error())
		os.Exit(1)
	}
}
