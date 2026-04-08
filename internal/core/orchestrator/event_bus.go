// event_bus.go implements the EventBus Restate Virtual Object.
//
// The EventBus is the single entrypoint for all CloudEvent emission in the
// orchestrator. It validates events against CUE schemas, delegates sequencing
// to the per-deployment EventStore, updates the global EventIndex, and fans
// out non-system events to the SinkRouter for external delivery.
//
// Using a Virtual Object (keyed by a global key) ensures single-writer
// semantics: all event emissions are serialised, preventing race conditions
// in schema validation and store sequencing.
package orchestrator

import (
	"fmt"
	"strings"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/cuevalidate"
)

// EventBus is the CloudEvents entrypoint. All deployment lifecycle events,
// command events, policy events, and drift events flow through Emit().
type EventBus struct {
	// schemaDir points to the directory containing CUE schema files used to
	// validate event payloads before they enter the pipeline.
	schemaDir string
}

// NewEventBus creates an EventBus that validates event payloads against CUE
// schemas in the given directory. Pass an empty string to skip validation.
func NewEventBus(schemaDir string) *EventBus {
	return &EventBus{schemaDir: schemaDir}
}

// ServiceName returns the Restate service name for the event bus object.
func (EventBus) ServiceName() string {
	return EventBusServiceName
}

// Emit validates, timestamps, and routes a CloudEvent through the pipeline.
//
// The flow is:
//  1. Validate required fields (source, type, deployment extension, workspace).
//  2. Auto-fill the timestamp via currentTime (journaled by Restate).
//  3. Validate the event payload against the CUE schema for its type.
//  4. Append to the per-deployment EventStore (assigns a sequence number).
//  5. Send to the global EventIndex for cross-deployment queries.
//  6. If not a system event, fan out to the SinkRouter for external delivery.
//
// Validation failures are terminal errors (HTTP 400) because they indicate
// programming bugs, not transient infrastructure issues.
func (b EventBus) Emit(ctx restate.ObjectContext, event cloudevents.Event) error {
	if strings.TrimSpace(event.Source()) == "" {
		return restate.TerminalError(fmt.Errorf("CloudEvent source is required"), 400)
	}
	if strings.TrimSpace(event.Type()) == "" {
		return restate.TerminalError(fmt.Errorf("CloudEvent type is required"), 400)
	}
	if event.Time().IsZero() {
		now, err := currentTime(ctx)
		if err != nil {
			return err
		}
		event.SetTime(now)
	}
	deploymentKey := strings.TrimSpace(eventStringExtension(event, EventExtensionDeployment))
	if deploymentKey == "" {
		return restate.TerminalError(fmt.Errorf("CloudEvent %q is missing %q extension", event.Type(), EventExtensionDeployment), 400)
	}
	if strings.TrimSpace(eventStringExtension(event, EventExtensionWorkspace)) == "" {
		return restate.TerminalError(fmt.Errorf("CloudEvent %q is missing %q extension", event.Type(), EventExtensionWorkspace), 400)
	}
	if err := b.validateEventData(event); err != nil {
		return restate.TerminalError(fmt.Errorf("event data validation failed for %s: %w", event.Type(), err), 400)
	}
	// Delegate sequencing to the per-deployment event store, which assigns
	// a monotonic sequence number and persists the event in a chunked stream.
	record, err := restate.WithRequestType[cloudevents.Event, SequencedCloudEvent](
		restate.Object[SequencedCloudEvent](ctx, DeploymentEventStoreServiceName, deploymentKey, "Append"),
	).Request(event)
	if err != nil {
		return err
	}
	// Fan out: index the event globally (for cross-deployment queries) and
	// route to sinks (for external delivery). Both are fire-and-forget sends;
	// failures in downstream processing don't block the event producer.
	restate.ObjectSend(ctx, EventIndexServiceName, EventIndexGlobalKey, "Index").Send(record)
	if !isSystemEventType(record.Event.Type()) {
		restate.ServiceSend(ctx, SinkRouterServiceName, "Deliver").Send(record)
	}
	return nil
}

// validateEventData checks the event's payload against the CUE schema for its
// type. Returns nil if no schema is configured or the event type has no schema.
func (b EventBus) validateEventData(event cloudevents.Event) error {
	if strings.TrimSpace(b.schemaDir) == "" {
		return nil
	}
	relativePath, definition, ok := eventSchemaForType(event.Type())
	if !ok {
		return nil
	}
	input := eventData(event)
	if input == nil {
		input = map[string]any{}
	}
	return cuevalidate.DecodeFile(b.schemaDir, relativePath, definition, input, nil)
}

// eventSchemaForType maps a CloudEvent type string to its CUE schema file path
// and definition name. Returns false for event types that have no schema.
func eventSchemaForType(eventType string) (string, string, bool) {
	switch eventType {
	case EventTypeDeploymentSubmitted:
		return "events/lifecycle.cue", "#DeploymentSubmittedData", true
	case EventTypeDeploymentStarted:
		return "events/lifecycle.cue", "#DeploymentStartedData", true
	case EventTypeDeploymentCompleted:
		return "events/lifecycle.cue", "#DeploymentCompletedData", true
	case EventTypeDeploymentFailed:
		return "events/lifecycle.cue", "#DeploymentFailedData", true
	case EventTypeDeploymentCancelled:
		return "events/lifecycle.cue", "#DeploymentCancelledData", true
	case EventTypeDeploymentDeleteStarted:
		return "events/lifecycle.cue", "#DeploymentDeleteStartedData", true
	case EventTypeDeploymentDeleteDone:
		return "events/lifecycle.cue", "#DeploymentDeleteCompletedData", true
	case EventTypeDeploymentDeleteFailed:
		return "events/lifecycle.cue", "#DeploymentDeleteFailedData", true
	case EventTypeCommandApply:
		return "events/command.cue", "#CommandApplyData", true
	case EventTypeCommandDelete:
		return "events/command.cue", "#CommandDeleteData", true
	case EventTypeCommandImport:
		return "events/command.cue", "#CommandImportData", true
	case EventTypeCommandCancel:
		return "events/command.cue", "#CommandCancelData", true
	case EventTypePolicyPreventedDestroy:
		return "events/policy.cue", "#PolicyPreventedDestroyData", true
	case EventTypeForceDeleteOverride:
		return "events/policy.cue", "#ForceDeleteOverrideData", true
	case EventTypeDriftDetected:
		return "events/drift.cue", "#DriftDetectedData", true
	case EventTypeDriftCorrected:
		return "events/drift.cue", "#DriftCorrectedData", true
	case EventTypeDriftExternalDelete:
		return "events/drift.cue", "#DriftExternalDeleteData", true
	case EventTypeResourceReplaceStarted:
		return "events/lifecycle.cue", "#ResourceReplaceStartedData", true
	case EventTypeResourceAutoReplaceStarted:
		return "events/lifecycle.cue", "#ResourceAutoReplaceStartedData", true
	case EventTypeResourceDispatched:
		return "events/lifecycle.cue", "#ResourceDispatchedData", true
	case EventTypeResourceReady:
		return "events/lifecycle.cue", "#ResourceReadyData", true
	case EventTypeResourceError:
		return "events/lifecycle.cue", "#ResourceErrorData", true
	case EventTypeResourceSkipped:
		return "events/lifecycle.cue", "#ResourceSkippedData", true
	case EventTypeResourceDeleteStarted:
		return "events/lifecycle.cue", "#ResourceDeleteStartedData", true
	case EventTypeResourceDeleted:
		return "events/lifecycle.cue", "#ResourceDeletedData", true
	case EventTypeResourceDeleteError:
		return "events/lifecycle.cue", "#ResourceDeleteErrorData", true
	case EventTypeSystemRetentionSweepStarted,
		EventTypeSystemRetentionSweepCompleted,
		EventTypeSystemRetentionShipFailed,
		EventTypeSystemRetentionShipCompleted:
		return "events/system.cue", "#RetentionEventData", true
	case EventTypeSystemSinkRegistered:
		return "events/system.cue", "#SinkRegisteredData", true
	case EventTypeSystemSinkRemoved:
		return "events/system.cue", "#SinkRemovedData", true
	case EventTypeSystemSinkDeliveryFailed:
		return "events/system.cue", "#SinkDeliveryFailedData", true
	default:
		return "", "", false
	}
}
