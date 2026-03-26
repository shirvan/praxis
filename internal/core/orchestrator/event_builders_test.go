package orchestrator

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventBuilders_ValidateCommandPolicyAndSystemEvents(t *testing.T) {
	absSchemaDir, err := filepath.Abs(filepath.Join("..", "..", "..", "schemas"))
	require.NoError(t, err)
	bus := NewEventBus(absSchemaDir)
	now := time.Now().UTC()

	applyEvent, err := NewCommandApplyEvent("dep-1", "prod", "default", 2, now)
	require.NoError(t, err)
	assert.Equal(t, EventTypeCommandApply, applyEvent.Type())
	require.NoError(t, bus.validateEventData(applyEvent))

	deleteEvent, err := NewCommandDeleteEvent("dep-1", "prod", 2, now)
	require.NoError(t, err)
	assert.Equal(t, EventTypeCommandDelete, deleteEvent.Type())
	require.NoError(t, bus.validateEventData(deleteEvent))

	importEvent, err := NewCommandImportEvent("resource-stream", "prod", "default", "us-east-1", "bucket-123", "S3Bucket", now)
	require.NoError(t, err)
	assert.Equal(t, EventTypeCommandImport, importEvent.Type())
	require.NoError(t, bus.validateEventData(importEvent))

	policyEvent, err := NewPolicyPreventedDestroyEvent("dep-1", "prod", 2, now, "bucket", "S3Bucket", "delete")
	require.NoError(t, err)
	assert.Equal(t, EventTypePolicyPreventedDestroy, policyEvent.Type())
	require.NoError(t, bus.validateEventData(policyEvent))

	record, err := NewResourceReadyEvent("dep-1", "prod", 2, now, "bucket", "S3Bucket", map[string]any{"bucketName": "demo"})
	require.NoError(t, err)
	sinkFailureEvent, err := NewSystemSinkDeliveryFailedEvent("audit-webhook", SinkTypeWebhook, SequencedCloudEvent{Sequence: 1, Event: record}, assert.AnError, now)
	require.NoError(t, err)
	assert.Equal(t, EventTypeSystemSinkDeliveryFailed, sinkFailureEvent.Type())
	require.NoError(t, bus.validateEventData(sinkFailureEvent))

	registeredEvent, err := NewSystemSinkRegisteredEvent("audit-webhook", SinkTypeWebhook, now)
	require.NoError(t, err)
	require.NoError(t, bus.validateEventData(registeredEvent))

	removedEvent, err := NewSystemSinkRemovedEvent("audit-webhook", now)
	require.NoError(t, err)
	require.NoError(t, bus.validateEventData(removedEvent))

	driftDetected, err := NewDriftDetectedEvent("dep-1", "prod", 2, "bucket", "S3Bucket", "tags changed")
	require.NoError(t, err)
	require.NoError(t, bus.validateEventData(driftDetected))

	driftCorrected, err := NewDriftCorrectedEvent("dep-1", "prod", 2, "bucket", "S3Bucket")
	require.NoError(t, err)
	require.NoError(t, bus.validateEventData(driftCorrected))

	driftExternalDelete, err := NewDriftExternalDeleteEvent("dep-1", "prod", 2, "bucket", "S3Bucket", "resource bucket was deleted externally")
	require.NoError(t, err)
	require.NoError(t, bus.validateEventData(driftExternalDelete))
}

func TestEventBuilders_PolicyPreventedDestroyCarriesExpectedFields(t *testing.T) {
	event, err := NewPolicyPreventedDestroyEvent("dep-2", "default", 1, time.Now().UTC(), "bucket", "S3Bucket", "force-replace")
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, event.DataAs(&payload))
	assert.Equal(t, "lifecycle.preventDestroy", payload["policy"])
	assert.Equal(t, "force-replace", payload["operation"])
	assert.Equal(t, "bucket", payload["resourceName"])
	assert.Equal(t, "S3Bucket", payload["resourceKind"])
	assert.Equal(t, EventCategoryPolicy, event.Extensions()[EventExtensionCategory])
	assert.Equal(t, EventSeverityWarn, event.Extensions()[EventExtensionSeverity])
}
