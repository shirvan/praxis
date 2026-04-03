// notification_sinks.go implements the notification sink configuration and
// delivery subsystem.
//
// The subsystem has two Restate services:
//
//   - NotificationSinkConfig (Virtual Object, global key): stores registered
//     sinks, validates them against CUE schemas, tracks delivery health with a
//     circuit breaker, and exposes CRUD + health endpoints.
//
//   - SinkRouter (stateless Service): receives sequenced CloudEvents from the
//     EventBus and fans them out to all matching sinks. Each delivery attempt
//     is wrapped in restate.Run for durable execution, with configurable retries
//     and exponential backoff.
//
// Sink types: webhook (HTTP POST), structured_log (stdout), cloudevents_http
// (HTTP POST with CE content-type), restate_rpc (Restate service-to-service).
//
// Circuit breaker: after 3 consecutive failures, a sink enters "open" state
// for 5 minutes. During this window, deliveries are silently skipped.
package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/cuevalidate"
	"github.com/shirvan/praxis/internal/core/resolver"
)

// NotificationSinkConfig is the Restate Virtual Object that stores and validates
// notification sink registrations. It uses a single global key so all sinks are
// managed atomically.
type NotificationSinkConfig struct {
	// schemaDir points to the CUE schema directory for sink validation.
	schemaDir string
}

// sinkDeliveryAttempt captures the result of a single delivery attempt inside
// a restate.Run block. The Error field is non-empty on failure.
type sinkDeliveryAttempt struct {
	Error string `json:"error,omitempty"`
}

// sinkHeaderResolver resolves SSM parameter references in sink headers.
type sinkHeaderResolver interface {
	Resolve(ctx restate.Context, rawSpecs map[string]json.RawMessage) (map[string]json.RawMessage, *resolver.SensitiveParams, error)
}

// sinkWorkspaceInfo carries workspace metadata needed to resolve the AWS
// account for SSM-backed sink headers.
type sinkWorkspaceInfo struct {
	Account string `json:"account"`
}

var newSinkHeaderResolver = func(ctx restate.Context, accountAlias string) (sinkHeaderResolver, error) {
	awsCfg, err := authservice.NewAuthClient().GetCredentials(ctx, accountAlias)
	if err != nil {
		return nil, err
	}
	return resolver.NewRestateSSMResolver(resolver.NewSSMResolver(ssm.NewFromConfig(awsCfg))), nil
}

// Circuit breaker constants.
const (
	// sinkCircuitBreakerThreshold is the number of consecutive failures before
	// the circuit opens.
	sinkCircuitBreakerThreshold = 3
	// sinkCircuitOpenDuration is how long the circuit stays open before
	// allowing delivery attempts again.
	sinkCircuitOpenDuration = 5 * time.Minute
)

// NewNotificationSinkConfig constructs the sink config object.
func NewNotificationSinkConfig(schemaDir string) *NotificationSinkConfig {
	return &NotificationSinkConfig{schemaDir: schemaDir}
}

// ServiceName returns the Restate service name for the sink config object.
func (*NotificationSinkConfig) ServiceName() string {
	return NotificationSinkConfigServiceName
}

// Upsert validates and stores a notification sink. CUE schema validation
// ensures the sink meets structural requirements. On update, runtime health
// fields (delivery counts, circuit breaker state) are preserved from the
// existing entry.
func (n *NotificationSinkConfig) Upsert(ctx restate.ObjectContext, sink NotificationSink) error {
	var normalized NotificationSink
	if err := cuevalidate.DecodeFile(n.schemaDir, "notifications/sink.cue", "#NotificationSink", sinkValidationInput(sink), &normalized); err != nil {
		return restate.TerminalError(fmt.Errorf("invalid sink config: %w", err), 400)
	}
	entries, err := restate.Get[map[string]NotificationSink](ctx, "entries")
	if err != nil {
		return err
	}
	if entries == nil {
		entries = make(map[string]NotificationSink)
	}
	normalized.Retry = normalizeRetryPolicy(normalized.Retry)
	if existing, ok := entries[normalized.Name]; ok {
		preserveSinkRuntime(&normalized, existing)
	}
	if strings.TrimSpace(normalized.DeliveryState) == "" {
		normalized.DeliveryState = SinkDeliveryStateHealthy
	}
	entries[normalized.Name] = normalized
	restate.Set(ctx, "entries", entries)
	event, eventErr := NewSystemSinkRegisteredEvent(normalized.Name, normalized.Type, time.Time{})
	if eventErr != nil {
		return eventErr
	}
	return EmitCloudEvent(ctx, event)
}

func sinkValidationInput(sink NotificationSink) map[string]any {
	input := map[string]any{
		"name": sink.Name,
		"type": sink.Type,
	}
	if strings.TrimSpace(sink.URL) != "" {
		input["url"] = sink.URL
	}
	if strings.TrimSpace(sink.Target) != "" {
		input["target"] = sink.Target
	}
	if strings.TrimSpace(sink.Handler) != "" {
		input["handler"] = sink.Handler
	}
	if len(sink.Headers) > 0 {
		input["headers"] = sink.Headers
	}
	if sink.Retry.MaxAttempts > 0 || sink.Retry.BackoffMs > 0 {
		input["retry"] = sink.Retry
	}
	if strings.TrimSpace(sink.ContentMode) != "" {
		input["contentMode"] = sink.ContentMode
	}
	if hasSinkFilter(sink.Filter) {
		input["filter"] = sink.Filter
	}
	return input
}

func hasSinkFilter(filter SinkFilter) bool {
	return len(filter.Types) > 0 ||
		len(filter.Categories) > 0 ||
		len(filter.Severities) > 0 ||
		len(filter.Workspaces) > 0 ||
		len(filter.Deployments) > 0
}

// Remove deletes a notification sink by name. Emits a sink.removed system event.
func (*NotificationSinkConfig) Remove(ctx restate.ObjectContext, name string) error {
	entries, err := restate.Get[map[string]NotificationSink](ctx, "entries")
	if err != nil {
		return err
	}
	if entries == nil {
		return nil
	}
	if _, ok := entries[name]; !ok {
		return nil
	}
	delete(entries, name)
	restate.Set(ctx, "entries", entries)
	event, eventErr := NewSystemSinkRemovedEvent(name, time.Time{})
	if eventErr != nil {
		return eventErr
	}
	return EmitCloudEvent(ctx, event)
}

// Get returns a single sink by name, or nil if not found.
func (*NotificationSinkConfig) Get(ctx restate.ObjectSharedContext, name string) (*NotificationSink, error) {
	entries, err := restate.Get[map[string]NotificationSink](ctx, "entries")
	if err != nil {
		return nil, err
	}
	if entries == nil {
		return nil, nil
	}
	sink, ok := entries[name]
	if !ok {
		return nil, nil
	}
	return &sink, nil
}

// List returns all sinks in deterministic order.
func (*NotificationSinkConfig) List(ctx restate.ObjectSharedContext) ([]NotificationSink, error) {
	entries, err := restate.Get[map[string]NotificationSink](ctx, "entries")
	if err != nil {
		return nil, err
	}
	return stableSinkList(entries), nil
}

// UpdateDeliveryState records a delivery success or failure for a named sink,
// updating delivery counters and circuit breaker state. On failure, if
// consecutive failures reach the threshold, the circuit opens for a cooldown.
func (*NotificationSinkConfig) UpdateDeliveryState(ctx restate.ObjectContext, update SinkDeliveryUpdate) error {
	if strings.TrimSpace(update.Name) == "" {
		return restate.TerminalError(fmt.Errorf("sink name is required"), 400)
	}
	entries, err := restate.Get[map[string]NotificationSink](ctx, "entries")
	if err != nil {
		return err
	}
	if entries == nil {
		return nil
	}
	sink, ok := entries[update.Name]
	if !ok {
		return nil
	}
	occurredAt := strings.TrimSpace(update.OccurredAt)
	if update.Succeeded {
		sink.DeliveredCount++
		sink.ConsecutiveFailures = 0
		sink.LastSuccessAt = occurredAt
		sink.LastError = ""
		sink.DeliveryState = SinkDeliveryStateHealthy
		sink.CircuitOpenedAt = ""
		sink.CircuitOpenUntil = ""
	} else {
		sink.FailedCount++
		sink.ConsecutiveFailures++
		sink.LastFailureAt = occurredAt
		sink.LastError = strings.TrimSpace(update.Error)
		if sink.ConsecutiveFailures >= sinkCircuitBreakerThreshold {
			sink.DeliveryState = SinkDeliveryStateOpen
			sink.CircuitOpenedAt = occurredAt
			if openedAt, parseErr := time.Parse(time.RFC3339, occurredAt); parseErr == nil {
				sink.CircuitOpenUntil = openedAt.Add(sinkCircuitOpenDuration).UTC().Format(time.RFC3339)
			}
		} else {
			sink.DeliveryState = SinkDeliveryStateDegraded
			sink.CircuitOpenedAt = ""
			sink.CircuitOpenUntil = ""
		}
	}
	entries[update.Name] = sink
	restate.Set(ctx, "entries", entries)
	return nil
}

// Health returns aggregate health stats across all registered sinks.
func (*NotificationSinkConfig) Health(ctx restate.ObjectSharedContext) (NotificationSinkHealth, error) {
	entries, err := restate.Get[map[string]NotificationSink](ctx, "entries")
	if err != nil {
		return NotificationSinkHealth{}, err
	}
	return summarizeSinkHealth(entries, time.Now().UTC()), nil
}

// SinkRouter is a stateless Restate Service that delivers CloudEvents to
// registered notification sinks. It fetches the current sink list from
// NotificationSinkConfig on each delivery request.
type SinkRouter struct{}

// ServiceName returns the Restate service name for the sink router.
func (SinkRouter) ServiceName() string {
	return SinkRouterServiceName
}

// Deliver fans out a single event to all matching sinks. Sinks whose filter
// doesn't match or whose circuit breaker is open are silently skipped.
// Delivery failures are logged but do not fail the overall operation.
func (SinkRouter) Deliver(ctx restate.Context, record SequencedCloudEvent) error {
	sinks, err := restate.Object[[]NotificationSink](ctx, NotificationSinkConfigServiceName, NotificationSinkConfigGlobalKey, "List").Request(restate.Void{})
	if err != nil {
		return err
	}
	now, err := currentTime(ctx)
	if err != nil {
		return err
	}
	for i := range sinks {
		if !sinkMatchesEvent(sinks[i].Filter, record.Event) {
			continue
		}
		if sinkCircuitOpen(sinks[i], now) {
			continue
		}
		if err := deliverToSink(ctx, sinks[i], record); err != nil {
			continue
		}
	}
	return nil
}

// DrainBatch delivers a batch of events to a specific named sink. Used by
// the retention system to ship events to a drain sink before deletion.
func (SinkRouter) DrainBatch(ctx restate.Context, req DrainBatchRequest) error {
	if strings.TrimSpace(req.SinkName) == "" {
		return restate.TerminalError(fmt.Errorf("sink name is required"), 400)
	}
	if len(req.Records) == 0 {
		return nil
	}
	sink, err := restate.WithRequestType[string, *NotificationSink](
		restate.Object[*NotificationSink](ctx, NotificationSinkConfigServiceName, NotificationSinkConfigGlobalKey, "Get"),
	).Request(req.SinkName)
	if err != nil {
		return err
	}
	if sink == nil {
		return restate.TerminalError(fmt.Errorf("sink %q not found", req.SinkName), 404)
	}
	now, err := currentTime(ctx)
	if err != nil {
		return err
	}
	if sinkCircuitOpen(*sink, now) {
		return nil
	}
	if err := deliverBatchWithRetry(ctx, *sink, req.Records); err != nil {
		return restate.TerminalError(err, 502)
	}
	return nil
}

// Test sends a synthetic test event to a named sink to verify connectivity.
// Returns an error if the sink's circuit is open or delivery fails.
func (SinkRouter) Test(ctx restate.Context, sinkName string) error {
	sink, err := restate.WithRequestType[string, *NotificationSink](
		restate.Object[*NotificationSink](ctx, NotificationSinkConfigServiceName, NotificationSinkConfigGlobalKey, "Get"),
	).Request(sinkName)
	if err != nil {
		return err
	}
	if sink == nil {
		return restate.TerminalError(fmt.Errorf("sink %q not found", sinkName), 404)
	}
	now, err := currentTime(ctx)
	if err != nil {
		return err
	}
	if sinkCircuitOpen(*sink, now) {
		until := sink.CircuitOpenUntil
		if until == "" {
			until = "the current cooldown window ends"
		}
		return restate.TerminalError(fmt.Errorf("sink %q circuit is open until %s", sinkName, until), 409)
	}
	event := cloudevents.NewEvent(cloudevents.VersionV1)
	event.SetID(fmt.Sprintf("test-%d", now.UnixNano()))
	event.SetSource("/praxis/system/test")
	event.SetType("dev.praxis.system.sink.test")
	event.SetTime(now)
	event.SetExtension(EventExtensionDeployment, "test-deployment")
	event.SetExtension(EventExtensionWorkspace, "test")
	event.SetExtension(EventExtensionGeneration, int64(0))
	event.SetExtension(EventExtensionCategory, EventCategorySystem)
	event.SetExtension(EventExtensionSeverity, EventSeverityInfo)
	if err := event.SetData(cloudevents.ApplicationJSON, map[string]any{
		"message": "notification sink test event",
	}); err != nil {
		return err
	}
	if err := deliverToSink(ctx, *sink, SequencedCloudEvent{Event: event}); err != nil {
		return restate.TerminalError(err, 502)
	}
	return nil
}

// deliverToSink dispatches a single event to one sink with retry logic.
// For restate_rpc sinks, delivery is a Restate service-to-service send.
// For HTTP-based sinks, deliverWithRetry handles retries and backoff.
// On success or failure, the sink's delivery state is updated.
func deliverToSink(ctx restate.Context, sink NotificationSink, record SequencedCloudEvent) error {
	// restate_rpc sinks deliver via Restate service call instead of HTTP
	if sink.Type == SinkTypeRestateRPC && sink.Target != "" && sink.Handler != "" {
		restate.ServiceSend(ctx, sink.Target, sink.Handler).Send(record)
		now, nowErr := currentTime(ctx)
		if nowErr == nil {
			_ = recordSinkDeliveryState(ctx, SinkDeliveryUpdate{
				Name:       sink.Name,
				Succeeded:  true,
				OccurredAt: now.UTC().Format(time.RFC3339),
			})
		}
		return nil
	}
	err := deliverWithRetry(ctx, sink, record)
	now, nowErr := currentTime(ctx)
	if err == nil {
		if nowErr == nil {
			_ = recordSinkDeliveryState(ctx, SinkDeliveryUpdate{
				Name:       sink.Name,
				Succeeded:  true,
				OccurredAt: now.UTC().Format(time.RFC3339),
			})
		}
		return nil
	}
	if nowErr == nil {
		_ = recordSinkDeliveryState(ctx, SinkDeliveryUpdate{
			Name:       sink.Name,
			Succeeded:  false,
			OccurredAt: now.UTC().Format(time.RFC3339),
			Error:      err.Error(),
		})
	}
	if nowErr == nil {
		if event, eventErr := NewSystemSinkDeliveryFailedEvent(sink.Name, sink.Type, record, err, now); eventErr == nil {
			_ = EmitCloudEvent(ctx, event)
		}
	}
	return err
}

// deliverWithRetry attempts to deliver an event up to MaxAttempts times with
// linear backoff. Each attempt is wrapped in restate.Run so the result is
// durably journaled—on replay, successful attempts are not re-executed.
func deliverWithRetry(ctx restate.Context, sink NotificationSink, record SequencedCloudEvent) error {
	headers, err := resolveSinkHeaders(ctx, sink, eventStringExtension(record.Event, EventExtensionWorkspace))
	if err != nil {
		return err
	}
	policy := normalizeRetryPolicy(sink.Retry)
	var lastErr error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		result, err := restate.Run(ctx, func(restate.RunContext) (sinkDeliveryAttempt, error) {
			if deliveryErr := deliverToSinkOnce(sink, headers, record); deliveryErr != nil {
				return sinkDeliveryAttempt{Error: deliveryErr.Error()}, nil
			}
			return sinkDeliveryAttempt{}, nil
		})
		if err != nil {
			return err
		}
		if result.Error == "" {
			return nil
		}
		lastErr = errors.New(result.Error)
		if attempt == policy.MaxAttempts {
			break
		}
		if err := restate.Sleep(ctx, time.Duration(policy.BackoffMs*attempt)*time.Millisecond); err != nil {
			return err
		}
	}
	return lastErr
}

// deliverBatchWithRetry is the batch variant of deliverWithRetry, used by
// DrainBatch for retention event shipping.
func deliverBatchWithRetry(ctx restate.Context, sink NotificationSink, records []SequencedCloudEvent) error {
	headers, err := resolveSinkHeaders(ctx, sink, eventStringExtension(records[0].Event, EventExtensionWorkspace))
	if err != nil {
		return err
	}
	policy := normalizeRetryPolicy(sink.Retry)
	var lastErr error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		result, err := restate.Run(ctx, func(restate.RunContext) (sinkDeliveryAttempt, error) {
			if batchErr := deliverBatchToSink(sink, headers, records); batchErr != nil {
				return sinkDeliveryAttempt{Error: batchErr.Error()}, nil
			}
			return sinkDeliveryAttempt{}, nil
		})
		if err != nil {
			return err
		}
		if result.Error == "" {
			return nil
		}
		lastErr = errors.New(result.Error)
		if attempt == policy.MaxAttempts {
			break
		}
		if err := restate.Sleep(ctx, time.Duration(policy.BackoffMs*attempt)*time.Millisecond); err != nil {
			return err
		}
	}
	return lastErr
}

// preserveSinkRuntime copies runtime health fields from an existing sink entry
// to a newly validated one, so that config updates don't reset delivery stats.
func preserveSinkRuntime(target *NotificationSink, existing NotificationSink) {
	target.LastError = existing.LastError
	target.LastSuccessAt = existing.LastSuccessAt
	target.LastFailureAt = existing.LastFailureAt
	target.ConsecutiveFailures = existing.ConsecutiveFailures
	target.DeliveredCount = existing.DeliveredCount
	target.FailedCount = existing.FailedCount
	target.DeliveryState = existing.DeliveryState
	target.CircuitOpenedAt = existing.CircuitOpenedAt
	target.CircuitOpenUntil = existing.CircuitOpenUntil
}

// recordSinkDeliveryState persists a delivery outcome to the NotificationSinkConfig
// virtual object, updating delivery counters and circuit breaker state.
func recordSinkDeliveryState(ctx restate.Context, update SinkDeliveryUpdate) error {
	_, err := restate.WithRequestType[SinkDeliveryUpdate, restate.Void](
		restate.Object[restate.Void](ctx, NotificationSinkConfigServiceName, NotificationSinkConfigGlobalKey, "UpdateDeliveryState"),
	).Request(update)
	return err
}

// sinkCircuitOpen checks whether a sink's circuit breaker is currently open
// (delivery attempts should be skipped).
func sinkCircuitOpen(sink NotificationSink, now time.Time) bool {
	if strings.TrimSpace(sink.CircuitOpenUntil) == "" {
		return false
	}
	openUntil, err := time.Parse(time.RFC3339, sink.CircuitOpenUntil)
	if err != nil {
		return false
	}
	return openUntil.After(now)
}

// summarizeSinkHealth builds aggregate health stats for the Health endpoint.
func summarizeSinkHealth(entries map[string]NotificationSink, now time.Time) NotificationSinkHealth {
	health := NotificationSinkHealth{Total: len(entries)}
	stable := stableSinkList(entries)
	for i := range stable {
		switch {
		case sinkCircuitOpen(stable[i], now):
			health.Open++
		case stable[i].ConsecutiveFailures > 0 || stable[i].DeliveryState == SinkDeliveryStateDegraded:
			health.Degraded++
		default:
			health.Healthy++
		}
		last := latestSinkActivity(stable[i])
		if last != "" && last > health.LastDeliveryAt {
			health.LastDeliveryAt = last
		}
	}
	return health
}

func latestSinkActivity(sink NotificationSink) string {
	if sink.LastSuccessAt > sink.LastFailureAt {
		return sink.LastSuccessAt
	}
	return sink.LastFailureAt
}

// resolveSinkHeaders resolves SSM parameter references (ssm:///) in sink header
// values. If no headers contain SSM references, the raw headers are returned
// unchanged. Otherwise, the workspace's AWS account is used to build an SSM
// client for secret resolution.
func resolveSinkHeaders(ctx restate.Context, sink NotificationSink, workspaceName string) (map[string]string, error) {
	if len(sink.Headers) == 0 {
		return nil, nil
	}
	needsResolution := false
	for _, value := range sink.Headers {
		if strings.HasPrefix(strings.TrimSpace(value), "ssm:///") {
			needsResolution = true
			break
		}
	}
	if !needsResolution {
		return sink.Headers, nil
	}

	accountAlias := "default"
	workspaceName = strings.TrimSpace(workspaceName)
	if workspaceName != "" && workspaceName != "system" && workspaceName != "test" {
		info, err := restate.Object[sinkWorkspaceInfo](ctx, workspaceServiceName, workspaceName, "Get").Request(restate.Void{})
		if err != nil {
			return nil, fmt.Errorf("resolve workspace %q for sink header secrets: %w", workspaceName, err)
		}
		if strings.TrimSpace(info.Account) != "" {
			accountAlias = info.Account
		}
	}

	resolverInstance, err := newSinkHeaderResolver(ctx, accountAlias)
	if err != nil {
		return nil, fmt.Errorf("resolve sink header secrets with account %q: %w", accountAlias, err)
	}
	raw, err := json.Marshal(map[string]any{"headers": sink.Headers})
	if err != nil {
		return nil, fmt.Errorf("marshal sink headers for secret resolution: %w", err)
	}
	resolved, _, err := resolverInstance.Resolve(ctx, map[string]json.RawMessage{"headers": raw})
	if err != nil {
		return nil, fmt.Errorf("resolve sink header secrets: %w", err)
	}
	var decoded struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(resolved["headers"], &decoded); err != nil {
		return nil, fmt.Errorf("decode resolved sink headers: %w", err)
	}
	return decoded.Headers, nil
}

// deliverToSinkOnce performs a single delivery attempt to an HTTP-based sink.
// Structured-log sinks write to stdout; webhook and cloudevents_http sinks
// perform an HTTP POST with the appropriate content type.
func deliverToSinkOnce(sink NotificationSink, headers map[string]string, record SequencedCloudEvent) error {
	payload, err := json.Marshal(record.Event)
	if err != nil {
		return fmt.Errorf("marshal event for sink %q: %w", sink.Name, err)
	}

	switch sink.Type {
	case SinkTypeStructuredLog:
		log.Print(string(payload))
		return nil
	case SinkTypeWebhook, SinkTypeCloudEventsHTTP:
		contentType := "application/json"
		if sink.Type == SinkTypeCloudEventsHTTP {
			contentType = cloudevents.ApplicationCloudEventsJSON
		}
		request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, sink.URL, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("deliver sink %q: build request: %w", sink.Name, err)
		}
		request.Header.Set("Content-Type", contentType)
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		resp, err := http.DefaultClient.Do(request) //nolint:gosec // URL is operator-configured sink endpoint
		if err != nil {
			return fmt.Errorf("deliver sink %q: %w", sink.Name, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("deliver sink %q: unexpected HTTP %d", sink.Name, resp.StatusCode)
		}
		return nil
	default:
		return fmt.Errorf("unsupported sink type %q", sink.Type)
	}
}

// deliverBatchToSink delivers a slice of events one at a time to a sink.
// Used by the retention drain path.
func deliverBatchToSink(sink NotificationSink, headers map[string]string, records []SequencedCloudEvent) error {
	for _, record := range records {
		if err := deliverToSinkOnce(sink, headers, record); err != nil {
			return err
		}
	}
	return nil
}
