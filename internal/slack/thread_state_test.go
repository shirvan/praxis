package slack

import (
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"
)

func TestSlackThreadState_RecordAndGet(t *testing.T) {
	env := restatetest.Start(t,
		restate.Reflect(SlackThreadState{}),
	)
	client := env.Ingress()

	record := ThreadRecord{
		ChannelID:   "C123",
		ThreadTS:    "1234567890.123456",
		SessionKey:  "slack:thread:C123:1234567890.123456",
		WatchRuleID: "watch-001",
		EventID:     "evt-001",
		EventType:   "dev.praxis.deployment.failed",
		CreatedAt:   "2024-01-01T00:00:00Z",
	}

	key := record.ChannelID + ":" + record.ThreadTS

	// Record
	_, err := ingress.Object[ThreadRecord, restate.Void](
		client, SlackThreadStateServiceName, key, "RecordThread",
	).Request(t.Context(), record)
	if err != nil {
		t.Fatalf("RecordThread: %v", err)
	}

	// GetThreadTS
	ts, err := ingress.Object[restate.Void, *string](
		client, SlackThreadStateServiceName, key, "GetThreadTS",
	).Request(t.Context(), restate.Void{})
	if err != nil {
		t.Fatalf("GetThreadTS: %v", err)
	}
	if ts == nil || *ts != record.ThreadTS {
		t.Errorf("expected thread TS %q, got %v", record.ThreadTS, ts)
	}

	// GetRecord
	got, err := ingress.Object[restate.Void, *ThreadRecord](
		client, SlackThreadStateServiceName, key, "GetRecord",
	).Request(t.Context(), restate.Void{})
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil record")
	}
	if got.EventID != record.EventID {
		t.Errorf("expected event ID %q, got %q", record.EventID, got.EventID)
	}
	if got.SessionKey != record.SessionKey {
		t.Errorf("expected session key %q, got %q", record.SessionKey, got.SessionKey)
	}
}

func TestSlackThreadState_GetThreadTS_NotFound(t *testing.T) {
	env := restatetest.Start(t,
		restate.Reflect(SlackThreadState{}),
	)
	client := env.Ingress()

	ts, err := ingress.Object[restate.Void, *string](
		client, SlackThreadStateServiceName, "nonexistent:key", "GetThreadTS",
	).Request(t.Context(), restate.Void{})
	if err != nil {
		t.Fatalf("GetThreadTS: %v", err)
	}
	if ts != nil {
		t.Errorf("expected nil for nonexistent thread, got %q", *ts)
	}
}
