//go:build integration

package integration

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/eventing"
)

func setupDriverEventingEnv(t *testing.T, services ...any) *ingress.Client {
	t.Helper()
	absSchemaDir, err := filepath.Abs("../../schemas")
	require.NoError(t, err)

	reflected := []restate.ServiceDefinition{
		restate.Reflect(authservice.NewAuthService(authservice.LoadBootstrapFromEnv())),
		restate.Reflect(orchestrator.NewEventBus(absSchemaDir)),
		restate.Reflect(orchestrator.DeploymentEventStore{}),
		restate.Reflect(orchestrator.EventIndex{}),
		restate.Reflect(orchestrator.ResourceEventOwnerObj{}),
		restate.Reflect(orchestrator.ResourceEventBridge{}),
		restate.Reflect(orchestrator.SinkRouter{}),
		restate.Reflect(orchestrator.NewNotificationSinkConfig(absSchemaDir)),
	}
	for _, service := range services {
		reflected = append(reflected, restate.Reflect(service))
	}
	env := restatetest.Start(t, reflected...)
	return env.Ingress()
}

func registerDriftEventOwner(t *testing.T, client *ingress.Client, resourceKey, streamKey, resourceName, resourceKind string) {
	t.Helper()
	_, err := ingress.Object[eventing.ResourceEventOwner, restate.Void](
		client,
		eventing.ResourceEventOwnerServiceName,
		resourceKey,
		"Upsert",
	).Request(t.Context(), eventing.ResourceEventOwner{
		StreamKey:    streamKey,
		Workspace:    "integration",
		Generation:   1,
		ResourceName: resourceName,
		ResourceKind: resourceKind,
	})
	require.NoError(t, err)
}

func pollDriftEventTypes(t *testing.T, client *ingress.Client, streamKey string, expected ...string) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		records, err := ingress.Object[int64, []orchestrator.SequencedCloudEvent](
			client,
			orchestrator.DeploymentEventStoreServiceName,
			streamKey,
			"ListSince",
		).Request(t.Context(), 0)
		require.NoError(t, err)
		typesSeen := make([]string, 0, len(records))
		seen := make(map[string]bool, len(records))
		for _, record := range records {
			typesSeen = append(typesSeen, record.Event.Type())
			seen[record.Event.Type()] = true
		}
		complete := true
		for _, want := range expected {
			if !seen[want] {
				complete = false
				break
			}
		}
		if complete || time.Now().After(deadline) {
			return typesSeen
		}
		time.Sleep(200 * time.Millisecond)
	}
}
