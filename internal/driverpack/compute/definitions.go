// Package compute owns the production service inventory for the compute driver pack.
package compute

import (
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/driverpack/genericbinding"
	"github.com/shirvan/praxis/internal/drivers/ami"
	"github.com/shirvan/praxis/internal/drivers/ec2"
	"github.com/shirvan/praxis/internal/drivers/ecrpolicy"
	"github.com/shirvan/praxis/internal/drivers/ecrrepo"
	"github.com/shirvan/praxis/internal/drivers/ecscluster"
	"github.com/shirvan/praxis/internal/drivers/ekscluster"
	"github.com/shirvan/praxis/internal/drivers/esm"
	"github.com/shirvan/praxis/internal/drivers/keypair"
	"github.com/shirvan/praxis/internal/drivers/lambda"
	"github.com/shirvan/praxis/internal/drivers/lambdalayer"
	"github.com/shirvan/praxis/internal/drivers/lambdaperm"
)

// Definitions returns every Restate Virtual Object served by praxis-compute.
// Keeping the inventory outside main makes the exact production bindings
// available to conformance tests.
func Definitions(auth authservice.AuthClient) []restate.ServiceDefinition {
	rp := config.DefaultRetryPolicy()
	return []restate.ServiceDefinition{
		genericbinding.Reflect(ami.NewGenericAMIDriver(auth), rp),
		genericbinding.Reflect(keypair.NewGenericKeyPairDriver(auth), rp),
		genericbinding.Reflect(ec2.NewGenericEC2InstanceDriver(auth), rp),
		genericbinding.Reflect(ecrrepo.NewGenericECRRepositoryDriver(auth), rp),
		genericbinding.Reflect(ecrpolicy.NewGenericECRLifecyclePolicyDriver(auth), rp),
		genericbinding.Reflect(ecscluster.NewGenericECSClusterDriver(auth), rp),
		genericbinding.Reflect(ekscluster.NewGenericEKSClusterDriver(auth), rp),
		genericbinding.Reflect(esm.NewGenericEventSourceMappingDriver(auth), rp),
		genericbinding.Reflect(lambda.NewGenericLambdaFunctionDriver(auth), rp),
		genericbinding.Reflect(lambdalayer.NewGenericLambdaLayerDriver(auth), rp),
		genericbinding.Reflect(lambdaperm.NewGenericLambdaPermissionDriver(auth), rp),
	}
}
