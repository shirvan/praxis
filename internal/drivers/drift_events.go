package drivers

import (
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/eventing"
)

// ReportDriftEvent sends a one-way (fire-and-forget) drift report to the
// ResourceEventBridge service. Drivers call this from their Reconcile handler
// when they detect drift, correct it, or discover an external deletion.
//
// Because this uses restate.ServiceSend (one-way), the driver does not block
// waiting for the event bridge to process the report. Restate guarantees
// at-least-once delivery via its journal, so the report survives crashes.
//
// Parameters:
//   - resourceKind: the driver type string (e.g., "S3Bucket", "SecurityGroup")
//   - eventType: one of eventing.DriftEventDetected, DriftEventCorrected,
//     or DriftEventExternalDelete
//   - errorMessage: non-empty only when the reconcile check itself errored
func ReportDriftEvent(ctx restate.ObjectContext, resourceKind, eventType, errorMessage string) {
	restate.ServiceSend(ctx, eventing.ResourceEventBridgeServiceName, "ReportDrift").Send(eventing.DriftReportRequest{
		ResourceKey:  restate.Key(ctx),
		ResourceKind: resourceKind,
		EventType:    eventType,
		Error:        errorMessage,
	})
}
