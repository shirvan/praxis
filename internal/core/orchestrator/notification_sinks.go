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

type NotificationSinkConfig struct {
	schemaDir string
}

type sinkDeliveryAttempt struct {
	Error string `json:"error,omitempty"`
}

type sinkHeaderResolver interface {
	Resolve(ctx restate.Context, rawSpecs map[string]json.RawMessage) (map[string]json.RawMessage, *resolver.SensitiveParams, error)
}

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

const (
	sinkCircuitBreakerThreshold = 3
	sinkCircuitOpenDuration     = 5 * time.Minute
)

func NewNotificationSinkConfig(schemaDir string) *NotificationSinkConfig {
	return &NotificationSinkConfig{schemaDir: schemaDir}
}

func (*NotificationSinkConfig) ServiceName() string {
	return NotificationSinkConfigServiceName
}

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

func (*NotificationSinkConfig) List(ctx restate.ObjectSharedContext) ([]NotificationSink, error) {
	entries, err := restate.Get[map[string]NotificationSink](ctx, "entries")
	if err != nil {
		return nil, err
	}
	return stableSinkList(entries), nil
}

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

func (*NotificationSinkConfig) Health(ctx restate.ObjectSharedContext) (NotificationSinkHealth, error) {
	entries, err := restate.Get[map[string]NotificationSink](ctx, "entries")
	if err != nil {
		return NotificationSinkHealth{}, err
	}
	return summarizeSinkHealth(entries, time.Now().UTC()), nil
}

type SinkRouter struct{}

func (SinkRouter) ServiceName() string {
	return SinkRouterServiceName
}

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

func recordSinkDeliveryState(ctx restate.Context, update SinkDeliveryUpdate) error {
	_, err := restate.WithRequestType[SinkDeliveryUpdate, restate.Void](
		restate.Object[restate.Void](ctx, NotificationSinkConfigServiceName, NotificationSinkConfigGlobalKey, "UpdateDeliveryState"),
	).Request(update)
	return err
}

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

func deliverBatchToSink(sink NotificationSink, headers map[string]string, records []SequencedCloudEvent) error {
	for _, record := range records {
		if err := deliverToSinkOnce(sink, headers, record); err != nil {
			return err
		}
	}
	return nil
}
