// cloudevents.go defines the CloudEvents type constants, extension keys,
// sink types, and shared types used across the orchestrator's event pipeline.
//
// Every deployment event follows the CloudEvents v1 spec. Praxis adds custom
// extensions (deployment, workspace, generation, resourcekind, category, severity)
// so that consumers can filter and route events without parsing payloads.
//
// Events flow:  producer → EventBus.Emit → EventStore.Append → EventIndex.Index
//
//	→ SinkRouter.Deliver → external sinks
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
	// defaultEventChunkSize controls how many SequencedCloudEvents are stored
	// in a single Restate state key before the event store rotates to a new chunk.
	// Chunking keeps individual state reads bounded while allowing unbounded streams.
	defaultEventChunkSize = 100

	// --- CloudEvent extension keys ---
	// These are set on every Praxis event and available for sink filtering.

	EventExtensionDeployment   = "deployment"   // stable deployment key
	EventExtensionWorkspace    = "workspace"    // workspace name
	EventExtensionGeneration   = "generation"   // deployment generation (monotonic counter)
	EventExtensionResourceKind = "resourcekind" // driver kind of the resource, when applicable
	EventExtensionCategory     = "category"     // lifecycle | drift | policy | command | system
	EventExtensionSeverity     = "severity"     // info | warn | error

	// --- Event categories ---

	EventCategoryLifecycle = "lifecycle" // deployment and resource lifecycle transitions
	EventCategoryDrift     = "drift"     // drift detection and correction
	EventCategoryPolicy    = "policy"    // policy enforcement actions
	EventCategoryCommand   = "command"   // CLI/API command receipts
	EventCategorySystem    = "system"    // internal system events (sinks, retention)

	// --- Severity levels ---

	EventSeverityInfo  = "info"  // normal operations
	EventSeverityWarn  = "warn"  // degraded but recoverable
	EventSeverityError = "error" // failures requiring attention

	// --- Deployment lifecycle event types ---

	EventTypeDeploymentSubmitted     = "dev.praxis.deployment.submitted"      // apply request accepted
	EventTypeDeploymentStarted       = "dev.praxis.deployment.started"        // workflow execution began
	EventTypeDeploymentCompleted     = "dev.praxis.deployment.completed"      // all resources ready
	EventTypeDeploymentFailed        = "dev.praxis.deployment.failed"         // one or more resources failed
	EventTypeDeploymentCancelled     = "dev.praxis.deployment.cancelled"      // operator cancelled the run
	EventTypeDeploymentDeleteStarted = "dev.praxis.deployment.delete.started" // delete workflow began
	EventTypeDeploymentDeleteDone    = "dev.praxis.deployment.delete.completed"
	EventTypeDeploymentDeleteFailed  = "dev.praxis.deployment.delete.failed"

	// --- Command event types ---

	EventTypeCommandApply  = "dev.praxis.command.apply"  // apply command received
	EventTypeCommandDelete = "dev.praxis.command.delete" // delete command received
	EventTypeCommandImport = "dev.praxis.command.import" // import command received
	EventTypeCommandCancel = "dev.praxis.command.cancel" // cancel command received

	// --- Policy event types ---

	EventTypePolicyPreventedDestroy = "dev.praxis.policy.prevented_destroy"     // lifecycle.preventDestroy blocked a destroy
	EventTypeForceDeleteOverride    = "dev.praxis.policy.force_delete_override" // force flag overrode lifecycle.preventDestroy

	// --- Resource lifecycle event types ---

	EventTypeResourceDispatched         = "dev.praxis.resource.dispatched"           // driver call dispatched
	EventTypeResourceReady              = "dev.praxis.resource.ready"                // resource provisioned successfully
	EventTypeResourceError              = "dev.praxis.resource.error"                // resource provisioning failed
	EventTypeResourceSkipped            = "dev.praxis.resource.skipped"              // skipped due to dep failure / cancellation
	EventTypeResourceDeleteStarted      = "dev.praxis.resource.delete.started"       // resource delete dispatched
	EventTypeResourceDeleted            = "dev.praxis.resource.deleted"              // resource deleted successfully
	EventTypeResourceDeleteError        = "dev.praxis.resource.delete.error"         // resource delete failed
	EventTypeResourceReplaceStarted     = "dev.praxis.resource.replace.started"      // force-replace: delete before re-provision
	EventTypeResourceAutoReplaceStarted = "dev.praxis.resource.auto_replace.started" // auto-replace: 409 immutable conflict triggered delete+re-provision

	// --- Drift event types ---

	EventTypeDriftDetected       = "dev.praxis.drift.detected"        // driver detected spec/real drift
	EventTypeDriftCorrected      = "dev.praxis.drift.corrected"       // drift was auto-corrected
	EventTypeDriftExternalDelete = "dev.praxis.drift.external_delete" // resource deleted outside Praxis

	// --- System event types ---

	EventTypeSystemSinkRegistered          = "dev.praxis.system.sink.registered"           // notification sink added
	EventTypeSystemSinkRemoved             = "dev.praxis.system.sink.removed"              // notification sink removed
	EventTypeSystemSinkDeliveryFailed      = "dev.praxis.system.sink.delivery_failed"      // sink delivery failed after retries
	EventTypeSystemRetentionSweepStarted   = "dev.praxis.system.retention.sweep_started"   // retention sweep began
	EventTypeSystemRetentionSweepCompleted = "dev.praxis.system.retention.sweep_completed" // retention sweep finished
	EventTypeSystemRetentionShipFailed     = "dev.praxis.system.retention.ship_failed"     // drain-before-delete failed
	EventTypeSystemRetentionShipCompleted  = "dev.praxis.system.retention.ship_completed"  // drain-before-delete succeeded

	// --- Notification sink types ---

	SinkTypeWebhook         = "webhook"          // plain HTTP POST with JSON body
	SinkTypeStructuredLog   = "structured_log"   // writes to Go log.Print (stdout)
	SinkTypeCloudEventsHTTP = "cloudevents_http" // HTTP POST with CloudEvents content-type
	SinkTypeRestateRPC      = "restate_rpc"      // delivers via Restate service-to-service send
)

// SequencedCloudEvent pairs a CloudEvent with a monotonically increasing
// sequence number assigned by the per-deployment EventStore on Append.
// The sequence provides total ordering within a deployment's event stream.
type SequencedCloudEvent struct {
	Sequence int64             `json:"sequence"`
	Event    cloudevents.Event `json:"event"`
}

// EventRangeRequest specifies an inclusive sequence range for fetching events
// from a deployment's event store.
type EventRangeRequest struct {
	StartSequence int64 `json:"startSequence"`
	EndSequence   int64 `json:"endSequence"`
}

// EventQuery is the filter input to EventIndex.Query. All non-zero fields are
// AND-matched; events must satisfy every specified criterion. The Limit field
// returns only the N most recent matching events.
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

// eventStoreMeta tracks per-deployment event store bookkeeping. It is stored
// under the "meta" state key in the DeploymentEventStore virtual object.
// NextSequence is the last assigned sequence number; ChunkCount tracks how many
// numbered chunk keys exist; ActiveCount caches the live event total so Count()
// can return without scanning all chunks.
type eventStoreMeta struct {
	NextSequence int64 `json:"nextSequence"`
	ActiveCount  int64 `json:"activeCount,omitempty"`
	ChunkCount   int   `json:"chunkCount"`
	ChunkSize    int   `json:"chunkSize"`
}

// indexedEvent is the denormalised record stored in the global EventIndex.
// It caches frequently-queried fields (deployment, workspace, severity, time)
// so that Query can filter without deserialising the full CloudEvent each time.
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

// SinkFilter controls which events a notification sink receives. All non-empty
// fields are AND-matched: an event must pass every specified criterion.
// Types and Deployments support prefix/glob matching respectively;
// Categories, Severities, and Workspaces use exact string matching.
type SinkFilter struct {
	Types       []string `json:"types,omitempty"`
	Categories  []string `json:"categories,omitempty"`
	Severities  []string `json:"severities,omitempty"`
	Workspaces  []string `json:"workspaces,omitempty"`
	Deployments []string `json:"deployments,omitempty"`
}

// RetryPolicy configures delivery retries for a notification sink. Retries
// use linear backoff: each successive attempt waits attempt * BackoffMs.
type RetryPolicy struct {
	MaxAttempts int `json:"maxAttempts,omitempty"`
	BackoffMs   int `json:"backoffMs,omitempty"`
}

// Sink delivery states track the health of each notification sink.
// The circuit breaker pattern prevents repeated delivery attempts to
// consistently failing endpoints: after ConsecutiveFailures reaches the
// threshold (3), the sink enters "open" state for a cooldown window.
const (
	SinkDeliveryStateHealthy  = "healthy"  // recent deliveries succeeded
	SinkDeliveryStateDegraded = "degraded" // some failures, circuit still closed
	SinkDeliveryStateOpen     = "open"     // circuit breaker tripped, deliveries paused
)

// NotificationSink represents a registered event delivery target. Config fields
// (Name, Type, URL, Filter, Headers, Retry) are operator-provided; runtime
// fields (DeliveredCount, FailedCount, DeliveryState, CircuitOpenUntil) are
// maintained by the SinkRouter after each delivery attempt.
type NotificationSink struct {
	Name                   string            `json:"name"`
	Type                   string            `json:"type"`
	URL                    string            `json:"url,omitempty"`
	Target                 string            `json:"target,omitempty"`
	Handler                string            `json:"handler,omitempty"`
	Filter                 SinkFilter        `json:"filter,omitzero"`
	Headers                map[string]string `json:"headers,omitempty"`
	Retry                  RetryPolicy       `json:"retry,omitzero"`
	ContentMode            string            `json:"contentMode,omitempty"`
	CircuitOpenDurationSec int               `json:"circuitOpenDurationSec,omitempty"`
	LastError              string            `json:"lastError,omitempty"`
	LastSuccessAt          string            `json:"lastSuccessAt,omitempty"`
	LastFailureAt          string            `json:"lastFailureAt,omitempty"`
	ConsecutiveFailures    int               `json:"consecutiveFailures,omitempty"`
	DeliveredCount         int64             `json:"deliveredCount,omitempty"`
	FailedCount            int64             `json:"failedCount,omitempty"`
	DeliveryState          string            `json:"deliveryState,omitempty"`
	CircuitOpenedAt        string            `json:"circuitOpenedAt,omitempty"`
	CircuitOpenUntil       string            `json:"circuitOpenUntil,omitempty"`
}

// NotificationSinkHealth summarises the aggregate health of all registered sinks.
type NotificationSinkHealth struct {
	Total           int                 `json:"total"`
	Healthy         int                 `json:"healthy"`
	Degraded        int                 `json:"degraded"`
	Open            int                 `json:"open"`
	TotalDelivered  int64               `json:"totalDelivered"`
	TotalFailed     int64               `json:"totalFailed"`
	LastDeliveryAt  string              `json:"lastDeliveryAt,omitempty"`
	CircuitBreakers []SinkCircuitStatus `json:"circuitBreakers,omitempty"`
}

// SinkCircuitStatus reports the circuit breaker state for a single sink.
type SinkCircuitStatus struct {
	Name                string `json:"name"`
	State               string `json:"state"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	OpenUntil           string `json:"openUntil,omitempty"`
}

// SinkDeliveryUpdate records the outcome of a single delivery attempt. The
// NotificationSinkConfig virtual object uses this to maintain circuit breaker
// state and delivery counters.
type SinkDeliveryUpdate struct {
	Name       string `json:"name"`
	Succeeded  bool   `json:"succeeded"`
	OccurredAt string `json:"occurredAt,omitempty"`
	Error      string `json:"error,omitempty"`
}

// normalizeRetryPolicy fills in default retry values (3 attempts, 1s backoff)
// when the operator's configuration omits them.
func normalizeRetryPolicy(policy RetryPolicy) RetryPolicy {
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = 3
	}
	if policy.BackoffMs <= 0 {
		policy.BackoffMs = 1000
	}
	return policy
}

// eventChunkKey returns the Restate state key for the Nth event chunk.
// Chunks are 1-indexed: chunk-1, chunk-2, etc.
func eventChunkKey(index int) string {
	return fmt.Sprintf("chunk-%d", index)
}

// matchesEventQuery tests whether a single event satisfies all non-zero fields
// of an EventQuery. Used by EventIndex.Query to filter the in-memory index.
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

// limitCloudEvents returns at most the last `limit` events, preserving order.
// A zero or negative limit disables trimming.
func limitCloudEvents(events []SequencedCloudEvent, limit int) []SequencedCloudEvent {
	if limit <= 0 || len(events) <= limit {
		return events
	}
	trimmed := append([]SequencedCloudEvent(nil), events[len(events)-limit:]...)
	return trimmed
}

// eventStringExtension reads a CloudEvent extension value as a string,
// handling the various concrete types (string, int, int64, float64, Stringer)
// that the CloudEvents SDK may return after JSON round-tripping.
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

// eventData deserialises the CloudEvent's data payload into a generic map.
// Returns nil on empty or unparsable data.
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

// eventDataString is a convenience accessor that reads a single string field
// from the CloudEvent's data payload.
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

// sinkMatchesEvent tests whether a CloudEvent passes a SinkFilter. Types use
// prefix matching (allowing "dev.praxis.resource." to match all resource events),
// Deployments use path.Match glob patterns, and all other fields use exact match.
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

// stableSinkList returns notification sinks in deterministic alphabetical order.
// This ensures consistent iteration order for delivery fan-out.
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

// isSystemEventType returns true for internal system events (sink registrations,
// retention sweeps). System events are stored but not forwarded to notification
// sinks to avoid feedback loops.
func isSystemEventType(eventType string) bool {
	return strings.HasPrefix(eventType, "dev.praxis.system.")
}
