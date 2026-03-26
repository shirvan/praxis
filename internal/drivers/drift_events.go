package drivers

import (
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/eventing"
)

func ReportDriftEvent(ctx restate.ObjectContext, resourceKind, eventType, errorMessage string) {
	restate.ServiceSend(ctx, eventing.ResourceEventBridgeServiceName, "ReportDrift").Send(eventing.DriftReportRequest{
		ResourceKey:  restate.Key(ctx),
		ResourceKind: resourceKind,
		EventType:    eventType,
		Error:        errorMessage,
	})
}
