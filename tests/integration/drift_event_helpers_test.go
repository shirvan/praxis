//go:build integration

package integration

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/shirvan/praxis/internal/restatetest"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/eventing"
)

const driverRecoveryRecordStateKey = "external-delete-recovery"

type driverRecoveryRecord struct {
	Request orchestrator.ExternalDeleteRequest `json:"request"`
	Calls   int                                `json:"calls"`
}

// driverRecoverySink completes the driver-only event harness at Core's
// external-delete recovery boundary. These tests exercise driver reporting,
// bridge translation, and dispatch; full DeploymentState recovery behavior is
// covered by the Core lifecycle environment.
type driverRecoverySink struct{}

func (driverRecoverySink) ServiceName() string { return orchestrator.DeploymentStateServiceName }

func (driverRecoverySink) HandleExternalDelete(ctx restate.ObjectContext, req orchestrator.ExternalDeleteRequest) (orchestrator.RecoveryResult, error) {
	record, err := restate.Get[driverRecoveryRecord](ctx, driverRecoveryRecordStateKey)
	if err != nil {
		return orchestrator.RecoveryResult{}, err
	}
	record.Request = req
	record.Calls++
	restate.Set(ctx, driverRecoveryRecordStateKey, record)
	return orchestrator.RecoveryResult{Manual: true, Reason: "driver event harness recorded recovery dispatch"}, nil
}

func (driverRecoverySink) GetExternalDelete(ctx restate.ObjectSharedContext, _ restate.Void) (driverRecoveryRecord, error) {
	return restate.Get[driverRecoveryRecord](ctx, driverRecoveryRecordStateKey)
}

func setupDriverEventingEnv(t *testing.T, services ...any) *ingress.Client {
	return setupDriverEventingEnvWithRecovery(t, false, services...)
}

func setupDriverEventingEnvWithCoreRecovery(t *testing.T, services ...any) *ingress.Client {
	return setupDriverEventingEnvWithRecovery(t, true, services...)
}

func setupDriverEventingEnvWithRecovery(t *testing.T, coreRecovery bool, services ...any) *ingress.Client {
	t.Helper()
	absSchemaDir, err := filepath.Abs("../../schemas")
	require.NoError(t, err)

	reflected := []restate.ServiceDefinition{
		restate.Reflect(authservice.NewAuthService(authservice.LoadBootstrapFromEnv())),
		restate.Reflect(orchestrator.NewEventBus(absSchemaDir)),
		restate.Reflect(orchestrator.DeploymentEventStore{}),
		restate.Reflect(orchestrator.ResourceEventOwnerObj{}),
		restate.Reflect(orchestrator.ResourceEventBridge{}),
		restate.Reflect(orchestrator.SinkRouter{}),
		restate.Reflect(orchestrator.NewNotificationSinkConfig(absSchemaDir)),
	}
	if coreRecovery {
		reflected = append(reflected, restate.Reflect(orchestrator.DeploymentStateObj{}))
	} else {
		reflected = append(reflected, restate.Reflect(driverRecoverySink{}))
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
