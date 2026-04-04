package main

import (
	"context"
	"log/slog"
	"os"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/drivers/acmcert"
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
	rp := config.DefaultRetryPolicy()

	srv := server.NewRestate().
		Bind(restate.Reflect(acmcert.NewACMCertificateDriver(auth), rp)).
		Bind(restate.Reflect(alb.NewALBDriver(auth), rp)).
		Bind(restate.Reflect(nlb.NewNLBDriver(auth), rp)).
		Bind(restate.Reflect(targetgroup.NewTargetGroupDriver(auth), rp)).
		Bind(restate.Reflect(listener.NewListenerDriver(auth), rp)).
		Bind(restate.Reflect(listenerrule.NewListenerRuleDriver(auth), rp)).
		Bind(restate.Reflect(eip.NewElasticIPDriver(auth), rp)).
		Bind(restate.Reflect(igw.NewIGWDriver(auth), rp)).
		Bind(restate.Reflect(natgw.NewNATGatewayDriver(auth), rp)).
		Bind(restate.Reflect(nacl.NewNetworkACLDriver(auth), rp)).
		Bind(restate.Reflect(route53zone.NewHostedZoneDriver(auth), rp)).
		Bind(restate.Reflect(route53record.NewDNSRecordDriver(auth), rp)).
		Bind(restate.Reflect(route53healthcheck.NewHealthCheckDriver(auth), rp)).
		Bind(restate.Reflect(routetable.NewRouteTableDriver(auth), rp)).
		Bind(restate.Reflect(sg.NewSecurityGroupDriver(auth), rp)).
		Bind(restate.Reflect(subnet.NewSubnetDriver(auth), rp)).
		Bind(restate.Reflect(vpcpeering.NewVPCPeeringDriver(auth), rp)).
		Bind(restate.Reflect(vpc.NewVPCDriver(auth), rp))

	slog.Info("starting network driver pack", "addr", cfg.ListenAddr)
	if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
		slog.Error("network driver pack exited", "err", err.Error())
		os.Exit(1)
	}
}
