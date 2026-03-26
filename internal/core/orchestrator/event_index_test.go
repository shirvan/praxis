package orchestrator

import (
	"testing"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"
	"github.com/stretchr/testify/assert"
)

func TestEventIndex_IndexKeepsSameSequenceAcrossDeployments(t *testing.T) {
	env := restatetest.Start(t, restate.Reflect(EventIndex{}))

	first := newIndexedTestEvent(t, "dep-a", "default", EventTypeCommandApply)
	second := newIndexedTestEvent(t, "dep-b", "default", EventTypeCommandDelete)

	_, err := ingress.Object[SequencedCloudEvent, restate.Void](env.Ingress(), EventIndexServiceName, EventIndexGlobalKey, "Index").Request(t.Context(), SequencedCloudEvent{
		Sequence: 1,
		Event:    first,
	})
	assert.NoError(t, err)

	_, err = ingress.Object[SequencedCloudEvent, restate.Void](env.Ingress(), EventIndexServiceName, EventIndexGlobalKey, "Index").Request(t.Context(), SequencedCloudEvent{
		Sequence: 1,
		Event:    second,
	})
	assert.NoError(t, err)

	records, err := ingress.Object[EventQuery, []SequencedCloudEvent](env.Ingress(), EventIndexServiceName, EventIndexGlobalKey, "Query").Request(t.Context(), EventQuery{})
	assert.NoError(t, err)
	assert.Len(t, records, 2)
}

func newIndexedTestEvent(t *testing.T, deploymentKey, workspace, eventType string) cloudevents.Event {
	t.Helper()
	event := cloudevents.NewEvent(cloudevents.VersionV1)
	event.SetSource("/praxis/" + workspace + "/" + deploymentKey)
	event.SetType(eventType)
	event.SetTime(time.Now().UTC())
	event.SetExtension(EventExtensionDeployment, deploymentKey)
	event.SetExtension(EventExtensionWorkspace, workspace)
	event.SetExtension(EventExtensionGeneration, int64(1))
	event.SetExtension(EventExtensionCategory, EventCategoryCommand)
	event.SetExtension(EventExtensionSeverity, EventSeverityInfo)
	assert.NoError(t, event.SetData(cloudevents.ApplicationJSON, map[string]any{"message": eventType}))
	return event
}
