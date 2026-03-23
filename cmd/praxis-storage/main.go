package main

import (
	"context"
	"log/slog"
	"os"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"

	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/drivers/auroracluster"
	"github.com/shirvan/praxis/internal/drivers/dbparametergroup"
	"github.com/shirvan/praxis/internal/drivers/dbsubnetgroup"
	"github.com/shirvan/praxis/internal/drivers/ebs"
	"github.com/shirvan/praxis/internal/drivers/rdsinstance"
	"github.com/shirvan/praxis/internal/drivers/s3"
)

func main() {
	cfg := config.Load()

	srv := server.NewRestate().
		Bind(restate.Reflect(s3.NewS3BucketDriver(cfg.Auth()))).
		Bind(restate.Reflect(ebs.NewEBSVolumeDriver(cfg.Auth()))).
		Bind(restate.Reflect(dbsubnetgroup.NewDBSubnetGroupDriver(cfg.Auth()))).
		Bind(restate.Reflect(dbparametergroup.NewDBParameterGroupDriver(cfg.Auth()))).
		Bind(restate.Reflect(rdsinstance.NewRDSInstanceDriver(cfg.Auth()))).
		Bind(restate.Reflect(auroracluster.NewAuroraClusterDriver(cfg.Auth())))

	slog.Info("starting storage driver pack", "addr", cfg.ListenAddr)
	if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
		slog.Error("storage driver pack exited", "err", err.Error())
		os.Exit(1)
	}
}
