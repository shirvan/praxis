package slack

import (
	"fmt"
	"time"

	restate "github.com/restatedev/sdk-go"
)

// SlackThreadState is a Restate Virtual Object for thread persistence and dedupe.
// Two keying strategies are used:
//
//   - Dedup key ("thread:<eventID>:<ruleID>"): ensures an event+rule combo
//     creates at most one thread. RecordThread writes here.
//   - Reverse lookup key ("<channelID>:<threadTS>"): maps a Slack thread back
//     to the concierge session for message routing. SetReverseLookup writes here.
//
// The RecordThread handler creates the dedup entry and then sends a one-way
// message to SetReverseLookup for the reverse-lookup entry. This avoids
// recursion — SetReverseLookup does NOT issue further sends.
type SlackThreadState struct{}

func (SlackThreadState) ServiceName() string { return SlackThreadStateServiceName }

// RecordThread persists a new thread record and registers a reverse lookup entry.
// The reverse lookup is created via a one-way ObjectSend to a separate
// Virtual Object key (channelID:threadTS), enabling the Gateway's
// ThreadTracker to look up threads by their Slack coordinates.
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
// Returns a pointer so callers can distinguish "no record" (nil) from "empty thread_ts".
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
