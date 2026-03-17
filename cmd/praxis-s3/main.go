package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/restatedev/sdk-go/server"

	"github.com/praxiscloud/praxis/internal/core/config"
	"github.com/praxiscloud/praxis/internal/drivers/s3"

	restate "github.com/restatedev/sdk-go"
)

func main() {
	cfg := config.Load()
	drv := s3.NewS3BucketDriver(cfg.Auth())

	srv := server.NewRestate().
		Bind(restate.Reflect(drv))

	slog.Info("starting S3 driver", "addr", cfg.ListenAddr)
	if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
		slog.Error("S3 driver exited", "err", err.Error())
		os.Exit(1)
	}
}
