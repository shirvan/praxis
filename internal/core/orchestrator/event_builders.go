// event_builders.go provides typed constructor functions for every CloudEvent
// type emitted by the orchestrator.
//
// Each builder creates a fully formed CloudEvent with the correct type string,
// extensions (deployment, workspace, generation, category, severity), and a
// typed JSON payload. Builders are the single source of truth for event schema
// compliance—if the CUE schema validation in EventBus.Emit fails, the bug is
// in the builder, not in the caller.
//
// Time-zero convention: builders that receive a zero time.Time leave the event
// timestamp unset; the EventBus fills it in via currentTime (Restate-journaled)
// before persisting the event.
package orchestrator

import (
	"fmt"
	"strings"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"

	"github.com/shirvan/praxis/pkg/types"
)

// --- Event payload types ---
// Each payload struct matches the corresponding CUE schema definition
// in schemas/events/. Fields are JSON-serialised into the CloudEvent's data.

// deploymentEventPayload is the data payload for deployment lifecycle events
// (submitted, started, completed, failed, cancelled, delete variants).
type deploymentEventPayload struct {
	Message string `json:"message"`
	Status  string `json:"status,omitempty"`
	Error   string `json:"error,omitempty"`
}

// commandEventPayload is the data payload for CLI/API command events
// (apply, delete, import, cancel).
type commandEventPayload struct {
	Message    string `json:"message"`
	Action     string `json:"action"`
	Status     string `json:"status,omitempty"`
	Account    string `json:"account,omitempty"`
	ResourceID string `json:"resourceId,omitempty"`
	Region     string `json:"region,omitempty"`
}

// policyEventPayload is the data payload for policy enforcement events
// (e.g. lifecycle.preventDestroy blocking a destroy operation).
type policyEventPayload struct {
	Message      string `json:"message"`
	Policy       string `json:"policy"`
	Operation    string `json:"operation,omitempty"`
	ResourceName string `json:"resourceName,omitempty"`
	ResourceKind string `json:"resourceKind,omitempty"`
	Error        string `json:"error,omitempty"`
}

// sinkSystemEventPayload is the data payload for notification sink system
// events (registered, removed, delivery_failed).
type sinkSystemEventPayload struct {
	Message       string `json:"message"`
	SinkName      string `json:"sinkName"`
	SinkType      string `json:"sinkType,omitempty"`
	EventType     string `json:"eventType,omitempty"`
	DeploymentKey string `json:"deploymentKey,omitempty"`
	Error         string `json:"error,omitempty"`
}

// driftEventPayload is the data payload for drift detection events
// (detected, corrected, external_delete).
type driftEventPayload struct {
	Message      string `json:"message"`
	ResourceName string `json:"resourceName,omitempty"`
	ResourceKind string `json:"resourceKind,omitempty"`
	Error        string `json:"error,omitempty"`
}

// resourceEventPayload is the data payload for resource lifecycle events
// (dispatched, ready, error, skipped, delete variants, replace).
type resourceEventPayload struct {
	Message      string         `json:"message"`
	Status       string         `json:"status,omitempty"`
	ResourceName string         `json:"resourceName,omitempty"`
	ResourceKind string         `json:"resourceKind,omitempty"`
	Error        string         `json:"error,omitempty"`
	Outputs      map[string]any `json:"outputs,omitempty"`
}

// --- Deployment lifecycle event builders ---

// NewDeploymentSubmittedEvent creates the event emitted when an apply request
// is accepted and the deployment enters Pending.
func NewDeploymentSubmittedEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time) (cloudevents.Event, error) {
	return newPraxisCloudEvent(
		EventTypeDeploymentSubmitted,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		"",
		"",
		EventCategoryLifecycle,
		EventSeverityInfo,
		deploymentEventPayload{Message: "apply request accepted", Status: string(types.DeploymentPending)},
	)
}

// NewCommandApplyEvent creates the event emitted when the apply CLI command
// is received.
func NewCommandApplyEvent(deploymentKey, workspace, account string, generation int64, occurredAt time.Time) (cloudevents.Event, error) {
	return newPraxisCloudEvent(
		EventTypeCommandApply,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		"",
		"",
		EventCategoryCommand,
		EventSeverityInfo,
		commandEventPayload{Message: "apply command received", Action: "apply", Status: string(types.DeploymentPending), Account: strings.TrimSpace(account)},
	)
}

// NewDeploymentStartedEvent creates the event emitted when the apply workflow
// begins executing (transitions to Running).
func NewDeploymentStartedEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time) (cloudevents.Event, error) {
	return newPraxisCloudEvent(
		EventTypeDeploymentStarted,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		"",
		"",
		EventCategoryLifecycle,
		EventSeverityInfo,
		deploymentEventPayload{Message: "deployment workflow started", Status: string(types.DeploymentRunning)},
	)
}

// NewDeploymentTerminalEvent creates the appropriate terminal event based on
// the final deployment status (completed, failed, or cancelled). The event type
// and severity are automatically determined from the status.
func NewDeploymentTerminalEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time, finalStatus types.DeploymentStatus, errorMessage string) (cloudevents.Event, error) {
	eventType := EventTypeDeploymentFailed
	severity := EventSeverityError
	switch finalStatus {
	case types.DeploymentComplete:
		eventType = EventTypeDeploymentCompleted
		severity = EventSeverityInfo
	case types.DeploymentCancelled:
		eventType = EventTypeDeploymentCancelled
		severity = EventSeverityWarn
	}
	return newPraxisCloudEvent(
		eventType,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		"",
		"",
		EventCategoryLifecycle,
		severity,
		deploymentEventPayload{
			Message: fmt.Sprintf("deployment finished with status %s", finalStatus),
			Status:  string(finalStatus),
			Error:   errorMessage,
		},
	)
}

// NewDeploymentDeleteStartedEvent creates the event emitted when the delete
// workflow begins executing.
func NewDeploymentDeleteStartedEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time) (cloudevents.Event, error) {
	return newPraxisCloudEvent(
		EventTypeDeploymentDeleteStarted,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		"",
		"",
		EventCategoryLifecycle,
		EventSeverityInfo,
		deploymentEventPayload{Message: "deployment delete workflow started", Status: string(types.DeploymentDeleting)},
	)
}

// NewDeploymentDeleteTerminalEvent creates the terminal event for the delete
// workflow (delete.completed or delete.failed).
func NewDeploymentDeleteTerminalEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time, finalStatus types.DeploymentStatus, errorMessage string) (cloudevents.Event, error) {
	eventType := EventTypeDeploymentDeleteFailed
	severity := EventSeverityError
	if finalStatus == types.DeploymentDeleted {
		eventType = EventTypeDeploymentDeleteDone
		severity = EventSeverityInfo
	}
	return newPraxisCloudEvent(
		eventType,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		"",
		"",
		EventCategoryLifecycle,
		severity,
		deploymentEventPayload{
			Message: fmt.Sprintf("deployment delete finished with status %s", finalStatus),
			Status:  string(finalStatus),
			Error:   errorMessage,
		},
	)
}

// --- Command event builders ---

// NewCommandCancelEvent creates the event emitted when a cancellation is detected
// by the apply workflow's dispatch loop.
func NewCommandCancelEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time) (cloudevents.Event, error) {
	return newPraxisCloudEvent(
		EventTypeCommandCancel,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		"",
		"",
		EventCategoryCommand,
		EventSeverityWarn,
		deploymentEventPayload{Message: "deployment cancellation requested; draining in-flight resources", Status: string(types.DeploymentRunning)},
	)
}

// NewCommandDeleteEvent creates the event emitted when the delete CLI command
// is received.
func NewCommandDeleteEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time) (cloudevents.Event, error) {
	return newPraxisCloudEvent(
		EventTypeCommandDelete,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		"",
		"",
		EventCategoryCommand,
		EventSeverityInfo,
		commandEventPayload{Message: "delete command received", Action: "delete", Status: string(types.DeploymentDeleting)},
	)
}

// NewCommandImportEvent creates the event emitted when a resource import
// command is received.
func NewCommandImportEvent(resourceStreamKey, workspace, account, region, resourceID, resourceKind string, occurredAt time.Time) (cloudevents.Event, error) {
	return newPraxisCloudEvent(
		EventTypeCommandImport,
		resourceStreamKey,
		workspace,
		0,
		occurredAt,
		resourceID,
		resourceKind,
		EventCategoryCommand,
		EventSeverityInfo,
		commandEventPayload{Message: "import command received", Action: "import", Account: strings.TrimSpace(account), ResourceID: strings.TrimSpace(resourceID), Region: strings.TrimSpace(region)},
	)
}

// --- Policy event builders ---

// NewPolicyPreventedDestroyEvent creates the event emitted when a destroy
// operation is blocked by lifecycle.preventDestroy.
func NewPolicyPreventedDestroyEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time, resourceName, resourceKind, operation string) (cloudevents.Event, error) {
	return newPraxisCloudEvent(
		EventTypePolicyPreventedDestroy,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		resourceName,
		resourceKind,
		EventCategoryPolicy,
		EventSeverityWarn,
		policyEventPayload{
			Message:      fmt.Sprintf("destroy blocked by lifecycle.preventDestroy for %s", resourceName),
			Policy:       "lifecycle.preventDestroy",
			Operation:    strings.TrimSpace(operation),
			ResourceName: resourceName,
			ResourceKind: resourceKind,
			Error:        fmt.Sprintf("resource %s has lifecycle.preventDestroy enabled; refusing to %s", resourceName, strings.TrimSpace(operation)),
		},
	)
}

// --- Drift event builders ---

// NewDriftDetectedEvent creates the event emitted when a driver detects that
// a resource's actual state has drifted from the desired spec.
func NewDriftDetectedEvent(deploymentKey, workspace string, generation int64, resourceName, resourceKind, errorMessage string) (cloudevents.Event, error) {
	message := fmt.Sprintf("resource %s drift detected", resourceName)
	if strings.TrimSpace(errorMessage) != "" {
		message = fmt.Sprintf("resource %s drift detected: %s", resourceName, strings.TrimSpace(errorMessage))
	}
	return newPraxisCloudEvent(
		EventTypeDriftDetected,
		deploymentKey,
		workspace,
		generation,
		time.Time{},
		resourceName,
		resourceKind,
		EventCategoryDrift,
		EventSeverityWarn,
		driftEventPayload{Message: message, ResourceName: resourceName, ResourceKind: resourceKind, Error: strings.TrimSpace(errorMessage)},
	)
}

// NewDriftCorrectedEvent creates the event emitted when a driver successfully
// corrects a detected drift.
func NewDriftCorrectedEvent(deploymentKey, workspace string, generation int64, resourceName, resourceKind string) (cloudevents.Event, error) {
	return newPraxisCloudEvent(
		EventTypeDriftCorrected,
		deploymentKey,
		workspace,
		generation,
		time.Time{},
		resourceName,
		resourceKind,
		EventCategoryDrift,
		EventSeverityInfo,
		driftEventPayload{Message: fmt.Sprintf("resource %s drift corrected", resourceName), ResourceName: resourceName, ResourceKind: resourceKind},
	)
}

// NewDriftExternalDeleteEvent creates the event emitted when a driver discovers
// that a managed resource was deleted outside of Praxis.
func NewDriftExternalDeleteEvent(deploymentKey, workspace string, generation int64, resourceName, resourceKind, errorMessage string) (cloudevents.Event, error) {
	message := fmt.Sprintf("resource %s was deleted externally", resourceName)
	if strings.TrimSpace(errorMessage) != "" {
		message = strings.TrimSpace(errorMessage)
	}
	return newPraxisCloudEvent(
		EventTypeDriftExternalDelete,
		deploymentKey,
		workspace,
		generation,
		time.Time{},
		resourceName,
		resourceKind,
		EventCategoryDrift,
		EventSeverityError,
		driftEventPayload{Message: message, ResourceName: resourceName, ResourceKind: resourceKind, Error: strings.TrimSpace(errorMessage)},
	)
}

// --- System event builders ---

// NewSystemSinkRegisteredEvent creates the event emitted when a notification
// sink is added or updated.
func NewSystemSinkRegisteredEvent(sinkName, sinkType string, occurredAt time.Time) (cloudevents.Event, error) {
	return newPraxisCloudEvent(
		EventTypeSystemSinkRegistered,
		"__system__/sinks",
		"system",
		0,
		occurredAt,
		sinkName,
		"",
		EventCategorySystem,
		EventSeverityInfo,
		sinkSystemEventPayload{Message: "notification sink registered", SinkName: sinkName, SinkType: sinkType},
	)
}

// NewSystemSinkRemovedEvent creates the event emitted when a notification
// sink is removed.
func NewSystemSinkRemovedEvent(sinkName string, occurredAt time.Time) (cloudevents.Event, error) {
	return newPraxisCloudEvent(
		EventTypeSystemSinkRemoved,
		"__system__/sinks",
		"system",
		0,
		occurredAt,
		sinkName,
		"",
		EventCategorySystem,
		EventSeverityInfo,
		sinkSystemEventPayload{Message: "notification sink removed", SinkName: sinkName},
	)
}

// NewSystemSinkDeliveryFailedEvent creates the event emitted when a sink
// delivery attempt fails after exhausting all retries.
func NewSystemSinkDeliveryFailedEvent(sinkName, sinkType string, record SequencedCloudEvent, deliveryErr error, occurredAt time.Time) (cloudevents.Event, error) {
	errMsg := ""
	if deliveryErr != nil {
		errMsg = deliveryErr.Error()
	}
	return newPraxisCloudEvent(
		EventTypeSystemSinkDeliveryFailed,
		"__system__/sinks",
		"system",
		0,
		occurredAt,
		sinkName,
		"",
		EventCategorySystem,
		EventSeverityError,
		sinkSystemEventPayload{
			Message:       "notification sink delivery failed",
			SinkName:      sinkName,
			SinkType:      sinkType,
			EventType:     record.Event.Type(),
			DeploymentKey: eventStringExtension(record.Event, EventExtensionDeployment),
			Error:         errMsg,
		},
	)
}

// --- Resource lifecycle event builders ---

// NewResourceReplaceStartedEvent creates the event emitted when a force-replace
// operation begins (the existing resource is being deleted before re-provision).
func NewResourceReplaceStartedEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time, resourceName, resourceKind string) (cloudevents.Event, error) {
	return newResourceLifecycleEvent(
		EventTypeResourceReplaceStarted,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		resourceName,
		resourceKind,
		EventSeverityInfo,
		resourceEventPayload{Message: fmt.Sprintf("force-replacing %s: deleting before re-provision", resourceName), Status: string(types.DeploymentRunning), ResourceName: resourceName, ResourceKind: resourceKind},
	)
}

// NewResourceDispatchedEvent creates the event emitted when a resource's
// driver provisioning call is dispatched.
func NewResourceDispatchedEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time, resourceName, resourceKind string) (cloudevents.Event, error) {
	return newResourceLifecycleEvent(
		EventTypeResourceDispatched,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		resourceName,
		resourceKind,
		EventSeverityInfo,
		resourceEventPayload{Message: fmt.Sprintf("dispatched %s resource", resourceKind), Status: string(types.DeploymentRunning), ResourceName: resourceName, ResourceKind: resourceKind},
	)
}

// NewResourceReadyEvent creates the event emitted when a resource's driver
// call completes successfully. The outputs map contains the resource's
// provisioned attributes (e.g. ARN, ID) used for downstream expression hydration.
func NewResourceReadyEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time, resourceName, resourceKind string, outputs map[string]any) (cloudevents.Event, error) {
	return newResourceLifecycleEvent(
		EventTypeResourceReady,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		resourceName,
		resourceKind,
		EventSeverityInfo,
		resourceEventPayload{Message: fmt.Sprintf("resource %s is ready", resourceName), Status: string(types.DeploymentRunning), ResourceName: resourceName, ResourceKind: resourceKind, Outputs: outputs},
	)
}

// NewResourceErrorEvent creates the event emitted when a resource's driver
// call fails. This triggers dependent resources to be marked Skipped.
func NewResourceErrorEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time, resourceName, resourceKind string, status types.DeploymentStatus, errorMessage string) (cloudevents.Event, error) {
	return newResourceLifecycleEvent(
		EventTypeResourceError,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		resourceName,
		resourceKind,
		EventSeverityError,
		resourceEventPayload{Message: fmt.Sprintf("resource %s failed", resourceName), Status: string(status), ResourceName: resourceName, ResourceKind: resourceKind, Error: errorMessage},
	)
}

// NewResourceSkippedEvent creates the event emitted when a resource is skipped
// because a dependency failed or the deployment was cancelled.
func NewResourceSkippedEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time, resourceName, resourceKind string, status types.DeploymentStatus, message string) (cloudevents.Event, error) {
	return newResourceLifecycleEvent(
		EventTypeResourceSkipped,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		resourceName,
		resourceKind,
		EventSeverityWarn,
		resourceEventPayload{Message: message, Status: string(status), ResourceName: resourceName, ResourceKind: resourceKind},
	)
}

// NewResourceDeleteStartedEvent creates the event emitted when a resource's
// driver delete call is dispatched.
func NewResourceDeleteStartedEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time, resourceName, resourceKind string) (cloudevents.Event, error) {
	return newResourceLifecycleEvent(
		EventTypeResourceDeleteStarted,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		resourceName,
		resourceKind,
		EventSeverityInfo,
		resourceEventPayload{Message: fmt.Sprintf("deleting %s resource", resourceKind), Status: string(types.DeploymentDeleting), ResourceName: resourceName, ResourceKind: resourceKind},
	)
}

// NewResourceDeletedEvent creates the event emitted when a resource is
// successfully deleted.
func NewResourceDeletedEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time, resourceName, resourceKind string) (cloudevents.Event, error) {
	return newResourceLifecycleEvent(
		EventTypeResourceDeleted,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		resourceName,
		resourceKind,
		EventSeverityInfo,
		resourceEventPayload{Message: fmt.Sprintf("resource %s deleted", resourceName), Status: string(types.DeploymentDeleting), ResourceName: resourceName, ResourceKind: resourceKind},
	)
}

// NewResourceDeleteErrorEvent creates the event emitted when a resource's
// delete call fails.
func NewResourceDeleteErrorEvent(deploymentKey, workspace string, generation int64, occurredAt time.Time, resourceName, resourceKind, errorMessage string) (cloudevents.Event, error) {
	return newResourceLifecycleEvent(
		EventTypeResourceDeleteError,
		deploymentKey,
		workspace,
		generation,
		occurredAt,
		resourceName,
		resourceKind,
		EventSeverityError,
		resourceEventPayload{Message: fmt.Sprintf("resource %s failed to delete", resourceName), Status: string(types.DeploymentDeleting), ResourceName: resourceName, ResourceKind: resourceKind, Error: errorMessage},
	)
}

// --- Core event construction ---

// newPraxisCloudEvent is the internal factory that all builders delegate to.
// It sets the CloudEvent source (/praxis/<workspace>/<deployment>), type,
// timestamp, subject, and all Praxis-specific extensions. The data payload
// is JSON-serialised from the given any value.
func newPraxisCloudEvent(eventType, deploymentKey, workspace string, generation int64, occurredAt time.Time, subject, resourceKind, category, severity string, data any) (cloudevents.Event, error) {
	deploymentKey = strings.TrimSpace(deploymentKey)
	workspace = normalizeEventWorkspace(workspace)
	if deploymentKey == "" {
		return cloudevents.Event{}, fmt.Errorf("deployment key is required")
	}
	event := cloudevents.NewEvent(cloudevents.VersionV1)
	event.SetSource(fmt.Sprintf("/praxis/%s/%s", workspace, deploymentKey))
	event.SetType(eventType)
	if !occurredAt.IsZero() {
		event.SetTime(occurredAt.UTC())
	}
	if trimmed := strings.TrimSpace(subject); trimmed != "" {
		event.SetSubject(trimmed)
	}
	event.SetExtension(EventExtensionDeployment, deploymentKey)
	event.SetExtension(EventExtensionWorkspace, workspace)
	event.SetExtension(EventExtensionGeneration, generation)
	if trimmed := strings.TrimSpace(resourceKind); trimmed != "" {
		event.SetExtension(EventExtensionResourceKind, trimmed)
	}
	event.SetExtension(EventExtensionCategory, category)
	event.SetExtension(EventExtensionSeverity, severity)
	if err := event.SetData(cloudevents.ApplicationJSON, data); err != nil {
		return cloudevents.Event{}, fmt.Errorf("encode CloudEvent payload: %w", err)
	}
	return event, nil
}

// normalizeEventWorkspace returns "default" for empty workspace strings,
// ensuring every event has a non-empty workspace extension.
func normalizeEventWorkspace(workspace string) string {
	trimmed := strings.TrimSpace(workspace)
	if trimmed == "" {
		return "default"
	}
	return trimmed
}

func newResourceLifecycleEvent(eventType, deploymentKey, workspace string, generation int64, occurredAt time.Time, resourceName, resourceKind, severity string, data resourceEventPayload) (cloudevents.Event, error) {
	return newPraxisCloudEvent(eventType, deploymentKey, workspace, generation, occurredAt, resourceName, resourceKind, EventCategoryLifecycle, severity, data)
}
