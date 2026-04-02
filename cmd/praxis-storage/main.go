package main

import (
	"context"
	"log/slog"
	"os"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/drivers/auroracluster"
	"github.com/shirvan/praxis/internal/drivers/dbparametergroup"
	"github.com/shirvan/praxis/internal/drivers/dbsubnetgroup"
	"github.com/shirvan/praxis/internal/drivers/ebs"
	"github.com/shirvan/praxis/internal/drivers/rdsinstance"
	"github.com/shirvan/praxis/internal/drivers/s3"
	"github.com/shirvan/praxis/internal/drivers/snssub"
	"github.com/shirvan/praxis/internal/drivers/snstopic"
	"github.com/shirvan/praxis/internal/drivers/sqs"
	"github.com/shirvan/praxis/internal/drivers/sqspolicy"
)

func main() {
	cfg := config.Load()
	auth := authservice.NewAuthClient()

	srv := server.NewRestate().
		Bind(restate.Reflect(s3.NewS3BucketDriver(auth))).
		Bind(restate.Reflect(ebs.NewEBSVolumeDriver(auth))).
		Bind(restate.Reflect(dbsubnetgroup.NewDBSubnetGroupDriver(auth))).
		Bind(restate.Reflect(dbparametergroup.NewDBParameterGroupDriver(auth))).
		Bind(restate.Reflect(rdsinstance.NewRDSInstanceDriver(auth))).
		Bind(restate.Reflect(auroracluster.NewAuroraClusterDriver(auth))).
		Bind(restate.Reflect(snstopic.NewSNSTopicDriver(auth))).
		Bind(restate.Reflect(snssub.NewSNSSubscriptionDriver(auth))).
		Bind(restate.Reflect(sqs.NewSQSQueueDriver(auth))).
		Bind(restate.Reflect(sqspolicy.NewSQSQueuePolicyDriver(auth)))

	slog.Info("starting storage driver pack", "addr", cfg.ListenAddr)
	if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
		slog.Error("storage driver pack exited", "err", err.Error())
		os.Exit(1)
	}
}
