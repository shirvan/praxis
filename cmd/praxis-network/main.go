package main

import (
	"context"
	"log/slog"
	"os"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"

	"github.com/praxiscloud/praxis/internal/core/config"
	"github.com/praxiscloud/praxis/internal/drivers/eip"
	"github.com/praxiscloud/praxis/internal/drivers/igw"
	"github.com/praxiscloud/praxis/internal/drivers/nacl"
	"github.com/praxiscloud/praxis/internal/drivers/routetable"
	"github.com/praxiscloud/praxis/internal/drivers/sg"
	"github.com/praxiscloud/praxis/internal/drivers/vpc"
)

func main() {
	cfg := config.Load()

	srv := server.NewRestate().
		Bind(restate.Reflect(eip.NewElasticIPDriver(cfg.Auth()))).
		Bind(restate.Reflect(igw.NewIGWDriver(cfg.Auth()))).
		Bind(restate.Reflect(nacl.NewNetworkACLDriver(cfg.Auth()))).
		Bind(restate.Reflect(routetable.NewRouteTableDriver(cfg.Auth()))).
		Bind(restate.Reflect(sg.NewSecurityGroupDriver(cfg.Auth()))).
		Bind(restate.Reflect(vpc.NewVPCDriver(cfg.Auth())))

	slog.Info("starting network driver pack", "addr", cfg.ListenAddr)
	if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
		slog.Error("network driver pack exited", "err", err.Error())
		os.Exit(1)
	}
}
