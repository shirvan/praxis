// Package identity owns the production service inventory for the identity driver pack.
package identity

import (
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/driverpack/genericbinding"
	"github.com/shirvan/praxis/internal/drivers/iamgroup"
	"github.com/shirvan/praxis/internal/drivers/iaminstanceprofile"
	"github.com/shirvan/praxis/internal/drivers/iampolicy"
	"github.com/shirvan/praxis/internal/drivers/iamrole"
	"github.com/shirvan/praxis/internal/drivers/iamuser"
	"github.com/shirvan/praxis/internal/drivers/kmskey"
	"github.com/shirvan/praxis/internal/drivers/secret"
)

// Definitions returns every Restate Virtual Object served by praxis-identity.
func Definitions(auth authservice.AuthClient) []restate.ServiceDefinition {
	rp := config.DefaultRetryPolicy()
	return []restate.ServiceDefinition{
		genericbinding.Reflect(iamrole.NewGenericIAMRoleDriver(auth), rp),
		genericbinding.Reflect(iampolicy.NewGenericIAMPolicyDriver(auth), rp),
		genericbinding.Reflect(iamuser.NewGenericIAMUserDriver(auth), rp),
		genericbinding.Reflect(iamgroup.NewGenericIAMGroupDriver(auth), rp),
		genericbinding.Reflect(iaminstanceprofile.NewGenericIAMInstanceProfileDriver(auth), rp),
		genericbinding.Reflect(kmskey.NewGenericKMSKeyDriver(auth), rp),
		genericbinding.Reflect(secret.NewGenericSecretsManagerSecretDriver(auth), rp),
	}
}
