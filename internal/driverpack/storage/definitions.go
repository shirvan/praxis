// Package storage owns the production service inventory for the storage driver pack.
package storage

import (
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/driverpack/genericbinding"
	"github.com/shirvan/praxis/internal/drivers/auroracluster"
	"github.com/shirvan/praxis/internal/drivers/dbparametergroup"
	"github.com/shirvan/praxis/internal/drivers/dbsubnetgroup"
	"github.com/shirvan/praxis/internal/drivers/dynamodbtable"
	"github.com/shirvan/praxis/internal/drivers/ebs"
	"github.com/shirvan/praxis/internal/drivers/rdsinstance"
	"github.com/shirvan/praxis/internal/drivers/s3"
	"github.com/shirvan/praxis/internal/drivers/snssub"
	"github.com/shirvan/praxis/internal/drivers/snstopic"
	"github.com/shirvan/praxis/internal/drivers/sqs"
	"github.com/shirvan/praxis/internal/drivers/sqspolicy"
	"github.com/shirvan/praxis/internal/drivers/ssmparameter"
)

// Definitions returns every Restate Virtual Object served by praxis-storage.
func Definitions(auth authservice.AuthClient) []restate.ServiceDefinition {
	rp := config.DefaultRetryPolicy()
	return []restate.ServiceDefinition{
		genericbinding.Reflect(s3.NewGenericS3BucketDriver(auth), rp),
		genericbinding.Reflect(ebs.NewGenericEBSVolumeDriver(auth), rp),
		genericbinding.Reflect(dynamodbtable.NewGenericDynamoDBTableDriver(auth), rp),
		genericbinding.Reflect(dbsubnetgroup.NewGenericDBSubnetGroupDriver(auth), rp),
		genericbinding.Reflect(dbparametergroup.NewGenericDBParameterGroupDriver(auth), rp),
		genericbinding.Reflect(rdsinstance.NewGenericRDSInstanceDriver(auth), rp),
		genericbinding.Reflect(auroracluster.NewGenericAuroraClusterDriver(auth), rp),
		genericbinding.Reflect(snstopic.NewGenericSNSTopicDriver(auth), rp),
		genericbinding.Reflect(snssub.NewGenericSNSSubscriptionDriver(auth), rp),
		genericbinding.Reflect(sqs.NewGenericSQSQueueDriver(auth), rp),
		genericbinding.Reflect(sqspolicy.NewGenericSQSQueuePolicyDriver(auth), rp),
		genericbinding.Reflect(ssmparameter.NewGenericSSMParameterDriver(auth), rp),
	}
}
