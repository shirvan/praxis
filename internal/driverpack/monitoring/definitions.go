// Package monitoring owns the production service inventory for the monitoring driver pack.
package monitoring

import (
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/driverpack/genericbinding"
	"github.com/shirvan/praxis/internal/drivers/dashboard"
	"github.com/shirvan/praxis/internal/drivers/loggroup"
	"github.com/shirvan/praxis/internal/drivers/metricalarm"
)

// Definitions returns every Restate Virtual Object served by praxis-monitoring.
func Definitions(auth authservice.AuthClient) []restate.ServiceDefinition {
	rp := config.DefaultRetryPolicy()
	return []restate.ServiceDefinition{
		genericbinding.Reflect(loggroup.NewGenericLogGroupDriver(auth), rp),
		genericbinding.Reflect(metricalarm.NewGenericMetricAlarmDriver(auth), rp),
		genericbinding.Reflect(dashboard.NewGenericDashboardDriver(auth), rp),
	}
}
