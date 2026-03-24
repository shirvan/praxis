package main

import (
	"context"
	"log/slog"
	"os"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/drivers/alb"
	"github.com/shirvan/praxis/internal/drivers/eip"
	"github.com/shirvan/praxis/internal/drivers/igw"
	"github.com/shirvan/praxis/internal/drivers/listener"
	"github.com/shirvan/praxis/internal/drivers/listenerrule"
	"github.com/shirvan/praxis/internal/drivers/nacl"
	"github.com/shirvan/praxis/internal/drivers/natgw"
	"github.com/shirvan/praxis/internal/drivers/nlb"
	"github.com/shirvan/praxis/internal/drivers/route53healthcheck"
	"github.com/shirvan/praxis/internal/drivers/route53record"
	"github.com/shirvan/praxis/internal/drivers/route53zone"
	"github.com/shirvan/praxis/internal/drivers/routetable"
	"github.com/shirvan/praxis/internal/drivers/sg"
	"github.com/shirvan/praxis/internal/drivers/subnet"
	"github.com/shirvan/praxis/internal/drivers/targetgroup"
	"github.com/shirvan/praxis/internal/drivers/vpc"
	"github.com/shirvan/praxis/internal/drivers/vpcpeering"
)

func main() {
	cfg := config.Load()
	auth := authservice.NewAuthClient()

	srv := server.NewRestate().
		Bind(restate.Reflect(alb.NewALBDriver(auth))).
		Bind(restate.Reflect(nlb.NewNLBDriver(auth))).
		Bind(restate.Reflect(targetgroup.NewTargetGroupDriver(auth))).
		Bind(restate.Reflect(listener.NewListenerDriver(auth))).
		Bind(restate.Reflect(listenerrule.NewListenerRuleDriver(auth))).
		Bind(restate.Reflect(eip.NewElasticIPDriver(auth))).
		Bind(restate.Reflect(igw.NewIGWDriver(auth))).
		Bind(restate.Reflect(natgw.NewNATGatewayDriver(auth))).
		Bind(restate.Reflect(nacl.NewNetworkACLDriver(auth))).
		Bind(restate.Reflect(route53zone.NewHostedZoneDriver(auth))).
		Bind(restate.Reflect(route53record.NewDNSRecordDriver(auth))).
		Bind(restate.Reflect(route53healthcheck.NewHealthCheckDriver(auth))).
		Bind(restate.Reflect(routetable.NewRouteTableDriver(auth))).
		Bind(restate.Reflect(sg.NewSecurityGroupDriver(auth))).
		Bind(restate.Reflect(subnet.NewSubnetDriver(auth))).
		Bind(restate.Reflect(vpcpeering.NewVPCPeeringDriver(auth))).
		Bind(restate.Reflect(vpc.NewVPCDriver(auth)))

	slog.Info("starting network driver pack", "addr", cfg.ListenAddr)
	if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
		slog.Error("network driver pack exited", "err", err.Error())
		os.Exit(1)
	}
}
