//go:build integration

package integration

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/core/orchestrator"
)

// rpcSinkReceiver is a plain Restate service that records every CloudEvent the
// SinkRouter delivers to it, standing in for an external automation consumer.
type rpcSinkReceiver struct {
	mu       sync.Mutex
	received []string
}

func (r *rpcSinkReceiver) ServiceName() string { return "TestRPCSinkReceiver" }

func (r *rpcSinkReceiver) OnEvent(ctx restate.Context, record orchestrator.SequencedCloudEvent) (restate.Void, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.received = append(r.received, record.Event.Type())
	return restate.Void{}, nil
}

func (r *rpcSinkReceiver) seen() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]bool, len(r.received))
	for _, t := range r.received {
		out[t] = true
	}
	return out
}

// TestSinkRPC_RealDeploymentEventsDelivered verifies the full event path for
// restate_rpc sinks: a real deployment's lifecycle events flow through the
// EventBus and SinkRouter and arrive at the registered Restate service —
// previously only the sink Test handler and webhook delivery were covered.
func TestSinkRPC_RealDeploymentEventsDelivered(t *testing.T) {
	receiver := &rpcSinkReceiver{}
	env := setupCoreStack(t, restate.Reflect(receiver))

	_, err := ingress.Object[orchestrator.NotificationSink, restate.Void](
		env.ingress,
		orchestrator.NotificationSinkConfigServiceName,
		orchestrator.NotificationSinkConfigGlobalKey,
		"Upsert",
	).Request(t.Context(), orchestrator.NotificationSink{
		Name:    "test-rpc-sink",
		Type:    orchestrator.SinkTypeRestateRPC,
		Target:  "TestRPCSinkReceiver",
		Handler: "OnEvent",
	})
	require.NoError(t, err)

	bucketName := uniqueName(t, "rpcsink")
	deployKey := "test-rpc-sink-" + bucketName
	applyAndWaitComplete(t, env, deployKey, simpleS3Template(bucketName), false)

	deadline := time.Now().Add(30 * time.Second)
	for {
		seen := receiver.seen()
		if seen["dev.praxis.deployment.completed"] {
			assert.True(t, seen["dev.praxis.deployment.started"],
				"lifecycle start event should be delivered too, got %v", receiver.received)
			return
		}
		require.True(t, time.Now().Before(deadline),
			"deployment lifecycle events never reached the restate_rpc sink; got %v", receiver.received)
		time.Sleep(200 * time.Millisecond)
	}
}
