// Package network owns the production service inventory for the network driver pack.
package network

import (
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/driverpack/genericbinding"
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

// Definitions returns every Restate Virtual Object served by praxis-network.
func Definitions(auth authservice.AuthClient) []restate.ServiceDefinition {
	rp := config.DefaultRetryPolicy()
	return []restate.ServiceDefinition{
		genericbinding.Reflect(acmcert.NewGenericACMCertificateDriver(auth), rp),
		genericbinding.Reflect(alb.NewGenericALBDriver(auth), rp),
		genericbinding.Reflect(nlb.NewGenericNLBDriver(auth), rp),
		genericbinding.Reflect(targetgroup.NewGenericTargetGroupDriver(auth), rp),
		genericbinding.Reflect(listener.NewGenericListenerDriver(auth), rp),
		genericbinding.Reflect(listenerrule.NewGenericListenerRuleDriver(auth), rp),
		genericbinding.Reflect(eip.NewGenericElasticIPDriver(auth), rp),
		genericbinding.Reflect(igw.NewGenericIGWDriver(auth), rp),
		genericbinding.Reflect(natgw.NewGenericNATGatewayDriver(auth), rp),
		genericbinding.Reflect(nacl.NewGenericNetworkACLDriver(auth), rp),
		genericbinding.Reflect(route53zone.NewGenericHostedZoneDriver(auth), rp),
		genericbinding.Reflect(route53record.NewGenericDNSRecordDriver(auth), rp),
		genericbinding.Reflect(route53healthcheck.NewGenericHealthCheckDriver(auth), rp),
		genericbinding.Reflect(routetable.NewGenericRouteTableDriver(auth), rp),
		genericbinding.Reflect(sg.NewGenericSecurityGroupDriver(auth), rp),
		genericbinding.Reflect(subnet.NewGenericSubnetDriver(auth), rp),
		genericbinding.Reflect(vpcpeering.NewGenericVPCPeeringDriver(auth), rp),
		genericbinding.Reflect(vpc.NewGenericVPCDriver(auth), rp),
	}
}
