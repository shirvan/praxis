package orchestrator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/resolver"
)

type fakeSinkSecretResolver struct{}

func (fakeSinkSecretResolver) Resolve(_ restate.Context, rawSpecs map[string]json.RawMessage) (map[string]json.RawMessage, *resolver.SensitiveParams, error) {
	var payload struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(rawSpecs["headers"], &payload); err != nil {
		return nil, nil, err
	}
	for key, value := range payload.Headers {
		if strings.HasPrefix(value, "ssm:///") {
			payload.Headers[key] = "Bearer resolved-secret"
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	return map[string]json.RawMessage{"headers": encoded}, nil, nil
}

func TestSinkRouterDeliver_ResolvesSSMHeaders(t *testing.T) {
	previousFactory := newSinkHeaderResolver
	newSinkHeaderResolver = func(restate.Context, string) (sinkHeaderResolver, error) {
		return fakeSinkSecretResolver{}, nil
	}
	t.Cleanup(func() {
		newSinkHeaderResolver = previousFactory
	})

	absSchemaDir, err := filepath.Abs(filepath.Join("..", "..", "..", "schemas"))
	require.NoError(t, err)

	var authorizationHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizationHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	env := restatetest.Start(t,
		restate.Reflect(NewEventBus(absSchemaDir)),
		restate.Reflect(DeploymentEventStore{}),
		restate.Reflect(EventIndex{}),
		restate.Reflect(NewNotificationSinkConfig(absSchemaDir)),
		restate.Reflect(SinkRouter{}),
	)
	client := env.Ingress()

	_, err = ingress.Object[NotificationSink, restate.Void](
		client,
		NotificationSinkConfigServiceName,
		NotificationSinkConfigGlobalKey,
		"Upsert",
	).Request(t.Context(), NotificationSink{
		Name: "secret-webhook",
		Type: SinkTypeWebhook,
		URL:  server.URL,
		Headers: map[string]string{
			"Authorization": "ssm:///praxis/test/token",
		},
	})
	require.NoError(t, err)

	event := cloudevents.NewEvent(cloudevents.VersionV1)
	event.SetID("evt-1")
	event.SetSource("/praxis/default/test-deployment")
	event.SetType(EventTypeDeploymentStarted)
	event.SetTime(time.Now().UTC())
	event.SetExtension(EventExtensionDeployment, "test-deployment")
	event.SetExtension(EventExtensionWorkspace, "")
	event.SetExtension(EventExtensionGeneration, int64(1))
	event.SetExtension(EventExtensionCategory, EventCategoryLifecycle)
	event.SetExtension(EventExtensionSeverity, EventSeverityInfo)
	require.NoError(t, event.SetData(cloudevents.ApplicationJSON, map[string]any{"message": "started"}))

	_, err = ingress.Service[SequencedCloudEvent, restate.Void](
		client,
		SinkRouterServiceName,
		"Deliver",
	).Request(t.Context(), SequencedCloudEvent{Sequence: 1, Event: event})
	require.NoError(t, err)
	assert.Equal(t, "Bearer resolved-secret", authorizationHeader)
}

func TestSinkRouter_RestateRPC(t *testing.T) {
	absSchemaDir, err := filepath.Abs(filepath.Join("..", "..", "..", "schemas"))
	require.NoError(t, err)

	env := restatetest.Start(t,
		restate.Reflect(NewEventBus(absSchemaDir)),
		restate.Reflect(DeploymentEventStore{}),
		restate.Reflect(EventIndex{}),
		restate.Reflect(NewNotificationSinkConfig(absSchemaDir)),
		restate.Reflect(SinkRouter{}),
		restate.Reflect(rpcSinkTarget{}),
	)
	client := env.Ingress()

	// Register a restate_rpc sink
	_, err = ingress.Object[NotificationSink, restate.Void](
		client,
		NotificationSinkConfigServiceName,
		NotificationSinkConfigGlobalKey,
		"Upsert",
	).Request(t.Context(), NotificationSink{
		Name:    "test-rpc-sink",
		Type:    SinkTypeRestateRPC,
		Target:  "RPCSinkTarget",
		Handler: "Receive",
		Filter:  SinkFilter{},
	})
	require.NoError(t, err)

	event := cloudevents.NewEvent(cloudevents.VersionV1)
	event.SetID("evt-rpc-1")
	event.SetSource("/praxis/default/test-deployment")
	event.SetType(EventTypeDeploymentStarted)
	event.SetTime(time.Now().UTC())
	event.SetExtension(EventExtensionDeployment, "test-deployment")
	event.SetExtension(EventExtensionWorkspace, "")
	event.SetExtension(EventExtensionGeneration, int64(1))
	event.SetExtension(EventExtensionCategory, EventCategoryLifecycle)
	event.SetExtension(EventExtensionSeverity, EventSeverityInfo)
	require.NoError(t, event.SetData(cloudevents.ApplicationJSON, map[string]any{"message": "rpc test"}))

	_, err = ingress.Service[SequencedCloudEvent, restate.Void](
		client,
		SinkRouterServiceName,
		"Deliver",
	).Request(t.Context(), SequencedCloudEvent{Sequence: 1, Event: event})
	require.NoError(t, err)
	// The send is fire-and-forget, so we just verify no error occurred
}

// rpcSinkTarget is a minimal Restate service used as a target for restate_rpc sink tests.
type rpcSinkTarget struct{}

func (rpcSinkTarget) ServiceName() string { return "RPCSinkTarget" }

func (rpcSinkTarget) Receive(_ restate.Context, _ SequencedCloudEvent) error {
	return nil
}
