package slack

import (
	"fmt"
	"time"

	restate "github.com/restatedev/sdk-go"
)

// SlackThreadState is a Restate Virtual Object for thread persistence and dedupe.
type SlackThreadState struct{}

func (SlackThreadState) ServiceName() string { return SlackThreadStateServiceName }

// RecordThread persists a new thread record and registers a reverse lookup entry.
func (SlackThreadState) RecordThread(ctx restate.ObjectContext, record ThreadRecord) error {
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return err
	}
	record.CreatedAt = now
	restate.Set(ctx, "record", record)

	reverseKey := fmt.Sprintf("%s:%s", record.ChannelID, record.ThreadTS)
	restate.ObjectSend(ctx, SlackThreadStateServiceName, reverseKey, "SetReverseLookup").
		Send(record)

	return nil
}

// SetReverseLookup persists a thread record for reverse-lookup purposes only.
// It does NOT issue further ObjectSend calls — breaking the recursion chain.
func (SlackThreadState) SetReverseLookup(ctx restate.ObjectContext, record ThreadRecord) error {
	restate.Set(ctx, "record", record)
	return nil
}

// GetThreadTS returns the thread_ts if a record exists (nil if not). Used for dedupe.
func (SlackThreadState) GetThreadTS(ctx restate.ObjectSharedContext) (*string, error) {
	record, err := restate.Get[*ThreadRecord](ctx, "record")
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	return &record.ThreadTS, nil
}

// GetRecord returns the full thread record.
func (SlackThreadState) GetRecord(ctx restate.ObjectSharedContext) (*ThreadRecord, error) {
	return restate.Get[*ThreadRecord](ctx, "record")
}
