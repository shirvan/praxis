package orchestrator

import (
	"fmt"
	"strings"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/cuevalidate"
)

type EventBus struct {
	schemaDir string
}

func NewEventBus(schemaDir string) *EventBus {
	return &EventBus{schemaDir: schemaDir}
}

func (EventBus) ServiceName() string {
	return EventBusServiceName
}

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
	record, err := restate.WithRequestType[cloudevents.Event, SequencedCloudEvent](
		restate.Object[SequencedCloudEvent](ctx, DeploymentEventStoreServiceName, deploymentKey, "Append"),
	).Request(event)
	if err != nil {
		return err
	}
	restate.ObjectSend(ctx, EventIndexServiceName, EventIndexGlobalKey, "Index").Send(record)
	if !isSystemEventType(record.Event.Type()) {
		restate.ServiceSend(ctx, SinkRouterServiceName, "Deliver").Send(record)
	}
	return nil
}

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
	case EventTypeDriftDetected:
		return "events/drift.cue", "#DriftDetectedData", true
	case EventTypeDriftCorrected:
		return "events/drift.cue", "#DriftCorrectedData", true
	case EventTypeDriftExternalDelete:
		return "events/drift.cue", "#DriftExternalDeleteData", true
	case EventTypeResourceReplaceStarted:
		return "events/lifecycle.cue", "#ResourceReplaceStartedData", true
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
