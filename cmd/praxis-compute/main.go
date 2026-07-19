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
	computepack "github.com/shirvan/praxis/internal/driverpack/compute"
)

func main() {
	cfg := config.Load()
	auth := authservice.NewAuthClient()

	srv := server.NewRestate()
	for _, definition := range computepack.Definitions(auth) {
		srv.Bind(definition)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	slog.Info("starting compute driver pack", "addr", cfg.ListenAddr)
	err := srv.Start(ctx, cfg.ListenAddr)
	stop()
	if err != nil {
		slog.Error("compute driver pack exited", "err", err.Error())
		os.Exit(1)
	}
}
