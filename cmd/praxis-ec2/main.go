package main

import (
	"context"
	"log/slog"
	"os"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"

	"github.com/praxiscloud/praxis/internal/core/config"
	"github.com/praxiscloud/praxis/internal/drivers/ec2"
)

func main() {
	cfg := config.Load()
	drv := ec2.NewEC2InstanceDriver(cfg.Auth())

	srv := server.NewRestate().
		Bind(restate.Reflect(drv))

	slog.Info("starting EC2 driver", "addr", cfg.ListenAddr)
	if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
		slog.Error("EC2 driver exited", "err", err.Error())
		os.Exit(1)
	}
}
