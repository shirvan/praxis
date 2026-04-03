package orchestrator

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
)

const (
	defaultEventChunkSize = 100

	EventExtensionDeployment   = "deployment"
	EventExtensionWorkspace    = "workspace"
	EventExtensionGeneration   = "generation"
	EventExtensionResourceKind = "resourcekind"
	EventExtensionCategory     = "category"
	EventExtensionSeverity     = "severity"

	EventCategoryLifecycle = "lifecycle"
	EventCategoryDrift     = "drift"
	EventCategoryPolicy    = "policy"
	EventCategoryCommand   = "command"
	EventCategorySystem    = "system"

	EventSeverityInfo  = "info"
	EventSeverityWarn  = "warn"
	EventSeverityError = "error"

	EventTypeDeploymentSubmitted           = "dev.praxis.deployment.submitted"
	EventTypeDeploymentStarted             = "dev.praxis.deployment.started"
	EventTypeDeploymentCompleted           = "dev.praxis.deployment.completed"
	EventTypeDeploymentFailed              = "dev.praxis.deployment.failed"
	EventTypeDeploymentCancelled           = "dev.praxis.deployment.cancelled"
	EventTypeDeploymentDeleteStarted       = "dev.praxis.deployment.delete.started"
	EventTypeDeploymentDeleteDone          = "dev.praxis.deployment.delete.completed"
	EventTypeDeploymentDeleteFailed        = "dev.praxis.deployment.delete.failed"
	EventTypeCommandApply                  = "dev.praxis.command.apply"
	EventTypeCommandDelete                 = "dev.praxis.command.delete"
	EventTypeCommandImport                 = "dev.praxis.command.import"
	EventTypeCommandCancel                 = "dev.praxis.command.cancel"
	EventTypePolicyPreventedDestroy        = "dev.praxis.policy.prevented_destroy"
	EventTypeResourceDispatched            = "dev.praxis.resource.dispatched"
	EventTypeResourceReady                 = "dev.praxis.resource.ready"
	EventTypeResourceError                 = "dev.praxis.resource.error"
	EventTypeResourceSkipped               = "dev.praxis.resource.skipped"
	EventTypeResourceDeleteStarted         = "dev.praxis.resource.delete.started"
	EventTypeResourceDeleted               = "dev.praxis.resource.deleted"
	EventTypeResourceDeleteError           = "dev.praxis.resource.delete.error"
	EventTypeResourceReplaceStarted        = "dev.praxis.resource.replace.started"
	EventTypeDriftDetected                 = "dev.praxis.drift.detected"
	EventTypeDriftCorrected                = "dev.praxis.drift.corrected"
	EventTypeDriftExternalDelete           = "dev.praxis.drift.external_delete"
	EventTypeSystemSinkRegistered          = "dev.praxis.system.sink.registered"
	EventTypeSystemSinkRemoved             = "dev.praxis.system.sink.removed"
	EventTypeSystemSinkDeliveryFailed      = "dev.praxis.system.sink.delivery_failed"
	EventTypeSystemRetentionSweepStarted   = "dev.praxis.system.retention.sweep_started"
	EventTypeSystemRetentionSweepCompleted = "dev.praxis.system.retention.sweep_completed"
	EventTypeSystemRetentionShipFailed     = "dev.praxis.system.retention.ship_failed"
	EventTypeSystemRetentionShipCompleted  = "dev.praxis.system.retention.ship_completed"

	SinkTypeWebhook         = "webhook"
	SinkTypeStructuredLog   = "structured_log"
	SinkTypeCloudEventsHTTP = "cloudevents_http"
	SinkTypeRestateRPC      = "restate_rpc"
)

type SequencedCloudEvent struct {
	Sequence int64             `json:"sequence"`
	Event    cloudevents.Event `json:"event"`
}

type EventRangeRequest struct {
	StartSequence int64 `json:"startSequence"`
	EndSequence   int64 `json:"endSequence"`
}

type EventQuery struct {
	DeploymentKey string    `json:"deploymentKey,omitempty"`
	Workspace     string    `json:"workspace,omitempty"`
	TypePrefix    string    `json:"typePrefix,omitempty"`
	Severity      string    `json:"severity,omitempty"`
	Resource      string    `json:"resource,omitempty"`
	Since         time.Time `json:"since,omitzero"`
	Until         time.Time `json:"until,omitzero"`
	Limit         int       `json:"limit,omitempty"`
}

type eventStoreMeta struct {
	NextSequence int64 `json:"nextSequence"`
	ActiveCount  int64 `json:"activeCount,omitempty"`
	ChunkCount   int   `json:"chunkCount"`
	ChunkSize    int   `json:"chunkSize"`
}

type indexedEvent struct {
	DeploymentKey string              `json:"deploymentKey"`
	Workspace     string              `json:"workspace,omitempty"`
	Sequence      int64               `json:"sequence"`
	Type          string              `json:"type"`
	Severity      string              `json:"severity,omitempty"`
	Subject       string              `json:"subject,omitempty"`
	Time          time.Time           `json:"time"`
	Record        SequencedCloudEvent `json:"record"`
}

type SinkFilter struct {
	Types       []string `json:"types,omitempty"`
	Categories  []string `json:"categories,omitempty"`
	Severities  []string `json:"severities,omitempty"`
	Workspaces  []string `json:"workspaces,omitempty"`
	Deployments []string `json:"deployments,omitempty"`
}

type RetryPolicy struct {
	MaxAttempts int `json:"maxAttempts,omitempty"`
	BackoffMs   int `json:"backoffMs,omitempty"`
}

const (
	SinkDeliveryStateHealthy  = "healthy"
	SinkDeliveryStateDegraded = "degraded"
	SinkDeliveryStateOpen     = "open"
)

type NotificationSink struct {
	Name                string            `json:"name"`
	Type                string            `json:"type"`
	URL                 string            `json:"url,omitempty"`
	Target              string            `json:"target,omitempty"`
	Handler             string            `json:"handler,omitempty"`
	Filter              SinkFilter        `json:"filter,omitzero"`
	Headers             map[string]string `json:"headers,omitempty"`
	Retry               RetryPolicy       `json:"retry,omitzero"`
	ContentMode         string            `json:"contentMode,omitempty"`
	LastError           string            `json:"lastError,omitempty"`
	LastSuccessAt       string            `json:"lastSuccessAt,omitempty"`
	LastFailureAt       string            `json:"lastFailureAt,omitempty"`
	ConsecutiveFailures int               `json:"consecutiveFailures,omitempty"`
	DeliveredCount      int64             `json:"deliveredCount,omitempty"`
	FailedCount         int64             `json:"failedCount,omitempty"`
	DeliveryState       string            `json:"deliveryState,omitempty"`
	CircuitOpenedAt     string            `json:"circuitOpenedAt,omitempty"`
	CircuitOpenUntil    string            `json:"circuitOpenUntil,omitempty"`
}

type NotificationSinkHealth struct {
	Total          int    `json:"total"`
	Healthy        int    `json:"healthy"`
	Degraded       int    `json:"degraded"`
	Open           int    `json:"open"`
	LastDeliveryAt string `json:"lastDeliveryAt,omitempty"`
}

type SinkDeliveryUpdate struct {
	Name       string `json:"name"`
	Succeeded  bool   `json:"succeeded"`
	OccurredAt string `json:"occurredAt,omitempty"`
	Error      string `json:"error,omitempty"`
}

func normalizeRetryPolicy(policy RetryPolicy) RetryPolicy {
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = 3
	}
	if policy.BackoffMs <= 0 {
		policy.BackoffMs = 1000
	}
	return policy
}

func eventChunkKey(index int) string {
	return fmt.Sprintf("chunk-%d", index)
}

func matchesEventQuery(record SequencedCloudEvent, query EventQuery) bool {
	event := record.Event
	if query.DeploymentKey != "" && eventStringExtension(event, EventExtensionDeployment) != query.DeploymentKey {
		return false
	}
	if query.Workspace != "" && eventStringExtension(event, EventExtensionWorkspace) != query.Workspace {
		return false
	}
	if query.TypePrefix != "" && !strings.HasPrefix(event.Type(), query.TypePrefix) {
		return false
	}
	if query.Severity != "" && eventStringExtension(event, EventExtensionSeverity) != query.Severity {
		return false
	}
	if query.Resource != "" && event.Subject() != query.Resource {
		return false
	}
	if !query.Since.IsZero() && event.Time().Before(query.Since) {
		return false
	}
	if !query.Until.IsZero() && event.Time().After(query.Until) {
		return false
	}
	return true
}

func limitCloudEvents(events []SequencedCloudEvent, limit int) []SequencedCloudEvent {
	if limit <= 0 || len(events) <= limit {
		return events
	}
	trimmed := append([]SequencedCloudEvent(nil), events[len(events)-limit:]...)
	return trimmed
}

func eventStringExtension(event cloudevents.Event, name string) string {
	value, ok := event.Extensions()[name]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	default:
		return fmt.Sprint(typed)
	}
}

func eventData(event cloudevents.Event) map[string]any {
	if len(event.Data()) == 0 {
		return nil
	}
	var payload map[string]any
	if err := event.DataAs(&payload); err != nil {
		return nil
	}
	return payload
}

func eventDataString(event cloudevents.Event, key string) string {
	payload := eventData(event)
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}

func sinkMatchesEvent(filter SinkFilter, event cloudevents.Event) bool {
	if len(filter.Types) > 0 {
		matched := false
		for _, prefix := range filter.Types {
			if strings.HasPrefix(event.Type(), prefix) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(filter.Categories) > 0 && !contains(filter.Categories, eventStringExtension(event, EventExtensionCategory)) {
		return false
	}
	if len(filter.Severities) > 0 && !contains(filter.Severities, eventStringExtension(event, EventExtensionSeverity)) {
		return false
	}
	if len(filter.Workspaces) > 0 && !contains(filter.Workspaces, eventStringExtension(event, EventExtensionWorkspace)) {
		return false
	}
	if len(filter.Deployments) > 0 {
		deployment := eventStringExtension(event, EventExtensionDeployment)
		matched := false
		for _, pattern := range filter.Deployments {
			ok, err := path.Match(pattern, deployment)
			if err == nil && ok {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func contains(values []string, target string) bool {
	return slices.Contains(values, target)
}

func stableSinkList(entries map[string]NotificationSink) []NotificationSink {
	if len(entries) == 0 {
		return nil
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]NotificationSink, 0, len(keys))
	for _, key := range keys {
		out = append(out, entries[key])
	}
	return out
}

func isSystemEventType(eventType string) bool {
	return strings.HasPrefix(eventType, "dev.praxis.system.")
}
