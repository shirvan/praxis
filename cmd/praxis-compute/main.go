package main

import (
	"context"
	"log/slog"
	"os"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/drivers/ami"
	"github.com/shirvan/praxis/internal/drivers/ec2"
	"github.com/shirvan/praxis/internal/drivers/ecrpolicy"
	"github.com/shirvan/praxis/internal/drivers/ecrrepo"
	"github.com/shirvan/praxis/internal/drivers/esm"
	"github.com/shirvan/praxis/internal/drivers/keypair"
	"github.com/shirvan/praxis/internal/drivers/lambda"
	"github.com/shirvan/praxis/internal/drivers/lambdalayer"
	"github.com/shirvan/praxis/internal/drivers/lambdaperm"
)

func main() {
	cfg := config.Load()
	auth := authservice.NewAuthClient()
	rp := config.DefaultRetryPolicy()

	srv := server.NewRestate().
		Bind(restate.Reflect(ami.NewAMIDriver(auth), rp)).
		Bind(restate.Reflect(keypair.NewKeyPairDriver(auth), rp)).
		Bind(restate.Reflect(ec2.NewEC2InstanceDriver(auth), rp)).
		Bind(restate.Reflect(ecrrepo.NewECRRepositoryDriver(auth), rp)).
		Bind(restate.Reflect(ecrpolicy.NewECRLifecyclePolicyDriver(auth), rp)).
		Bind(restate.Reflect(esm.NewEventSourceMappingDriver(auth), rp)).
		Bind(restate.Reflect(lambda.NewLambdaFunctionDriver(auth), rp)).
		Bind(restate.Reflect(lambdalayer.NewLambdaLayerDriver(auth), rp)).
		Bind(restate.Reflect(lambdaperm.NewLambdaPermissionDriver(auth), rp))

	slog.Info("starting compute driver pack", "addr", cfg.ListenAddr)
	if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
		slog.Error("compute driver pack exited", "err", err.Error())
		os.Exit(1)
	}
}
