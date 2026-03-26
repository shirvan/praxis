package orchestrator

import (
	"fmt"
	"strings"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"

	"github.com/shirvan/praxis/pkg/types"
)

type deploymentEventPayload struct {
	Message string `json:"message"`
	Status  string `json:"status,omitempty"`
	Error   string `json:"error,omitempty"`
}

type commandEventPayload struct {
	Message    string `json:"message"`
	Action     string `json:"action"`
	Status     string `json:"status,omitempty"`
	Account    string `json:"account,omitempty"`
	ResourceID string `json:"resourceId,omitempty"`
	Region     string `json:"region,omitempty"`
}

type policyEventPayload struct {
	Message      string `json:"message"`
	Policy       string `json:"policy"`
	Operation    string `json:"operation,omitempty"`
	ResourceName string `json:"resourceName,omitempty"`
	ResourceKind string `json:"resourceKind,omitempty"`
	Error        string `json:"error,omitempty"`
}

type sinkSystemEventPayload struct {
	Message       string `json:"message"`
	SinkName      string `json:"sinkName"`
	SinkType      string `json:"sinkType,omitempty"`
	EventType     string `json:"eventType,omitempty"`
	DeploymentKey string `json:"deploymentKey,omitempty"`
	Error         string `json:"error,omitempty"`
}

type driftEventPayload struct {
	Message      string `json:"message"`
	ResourceName string `json:"resourceName,omitempty"`
	ResourceKind string `json:"resourceKind,omitempty"`
	Error        string `json:"error,omitempty"`
}

type resourceEventPayload struct {
	Message      string         `json:"message"`
	Status       string         `json:"status,omitempty"`
	ResourceName string         `json:"resourceName,omitempty"`
	ResourceKind string         `json:"resourceKind,omitempty"`
	Error        string         `json:"error,omitempty"`
	Outputs      map[string]any `json:"outputs,omitempty"`
}

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
