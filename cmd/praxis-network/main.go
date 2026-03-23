package main

import (
	"context"
	"log/slog"
	"os"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"

	"github.com/praxiscloud/praxis/internal/core/config"
	"github.com/praxiscloud/praxis/internal/drivers/alb"
	"github.com/praxiscloud/praxis/internal/drivers/eip"
	"github.com/praxiscloud/praxis/internal/drivers/igw"
	"github.com/praxiscloud/praxis/internal/drivers/listener"
	"github.com/praxiscloud/praxis/internal/drivers/listenerrule"
	"github.com/praxiscloud/praxis/internal/drivers/nacl"
	"github.com/praxiscloud/praxis/internal/drivers/natgw"
	"github.com/praxiscloud/praxis/internal/drivers/nlb"
	"github.com/praxiscloud/praxis/internal/drivers/route53healthcheck"
	"github.com/praxiscloud/praxis/internal/drivers/route53record"
	"github.com/praxiscloud/praxis/internal/drivers/route53zone"
	"github.com/praxiscloud/praxis/internal/drivers/routetable"
	"github.com/praxiscloud/praxis/internal/drivers/sg"
	"github.com/praxiscloud/praxis/internal/drivers/subnet"
	"github.com/praxiscloud/praxis/internal/drivers/targetgroup"
	"github.com/praxiscloud/praxis/internal/drivers/vpc"
	"github.com/praxiscloud/praxis/internal/drivers/vpcpeering"
)

func main() {
	cfg := config.Load()

	srv := server.NewRestate().
		Bind(restate.Reflect(alb.NewALBDriver(cfg.Auth()))).
		Bind(restate.Reflect(nlb.NewNLBDriver(cfg.Auth()))).
		Bind(restate.Reflect(targetgroup.NewTargetGroupDriver(cfg.Auth()))).
		Bind(restate.Reflect(listener.NewListenerDriver(cfg.Auth()))).
		Bind(restate.Reflect(listenerrule.NewListenerRuleDriver(cfg.Auth()))).
		Bind(restate.Reflect(eip.NewElasticIPDriver(cfg.Auth()))).
		Bind(restate.Reflect(igw.NewIGWDriver(cfg.Auth()))).
		Bind(restate.Reflect(natgw.NewNATGatewayDriver(cfg.Auth()))).
		Bind(restate.Reflect(nacl.NewNetworkACLDriver(cfg.Auth()))).
		Bind(restate.Reflect(route53zone.NewHostedZoneDriver(cfg.Auth()))).
		Bind(restate.Reflect(route53record.NewDNSRecordDriver(cfg.Auth()))).
		Bind(restate.Reflect(route53healthcheck.NewHealthCheckDriver(cfg.Auth()))).
		Bind(restate.Reflect(routetable.NewRouteTableDriver(cfg.Auth()))).
		Bind(restate.Reflect(sg.NewSecurityGroupDriver(cfg.Auth()))).
		Bind(restate.Reflect(subnet.NewSubnetDriver(cfg.Auth()))).
		Bind(restate.Reflect(vpcpeering.NewVPCPeeringDriver(cfg.Auth()))).
		Bind(restate.Reflect(vpc.NewVPCDriver(cfg.Auth())))

	slog.Info("starting network driver pack", "addr", cfg.ListenAddr)
	if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
		slog.Error("network driver pack exited", "err", err.Error())
		os.Exit(1)
	}
}
