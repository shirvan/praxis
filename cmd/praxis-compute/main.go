package main

import (
	"context"
	"log/slog"
	"os"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"

	"github.com/praxiscloud/praxis/internal/core/config"
	"github.com/praxiscloud/praxis/internal/drivers/ami"
	"github.com/praxiscloud/praxis/internal/drivers/ec2"
	"github.com/praxiscloud/praxis/internal/drivers/esm"
	"github.com/praxiscloud/praxis/internal/drivers/keypair"
	"github.com/praxiscloud/praxis/internal/drivers/lambda"
	"github.com/praxiscloud/praxis/internal/drivers/lambdalayer"
	"github.com/praxiscloud/praxis/internal/drivers/lambdaperm"
)

func main() {
	cfg := config.Load()

	srv := server.NewRestate().
		Bind(restate.Reflect(ami.NewAMIDriver(cfg.Auth()))).
		Bind(restate.Reflect(keypair.NewKeyPairDriver(cfg.Auth()))).
		Bind(restate.Reflect(ec2.NewEC2InstanceDriver(cfg.Auth()))).
		Bind(restate.Reflect(esm.NewEventSourceMappingDriver(cfg.Auth()))).
		Bind(restate.Reflect(lambda.NewLambdaFunctionDriver(cfg.Auth()))).
		Bind(restate.Reflect(lambdalayer.NewLambdaLayerDriver(cfg.Auth()))).
		Bind(restate.Reflect(lambdaperm.NewLambdaPermissionDriver(cfg.Auth())))

	slog.Info("starting compute driver pack", "addr", cfg.ListenAddr)
	if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
		slog.Error("compute driver pack exited", "err", err.Error())
		os.Exit(1)
	}
}
