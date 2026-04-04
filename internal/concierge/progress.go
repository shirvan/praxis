// progress.go implements a lightweight progress-tracking Virtual Object for
// streaming tool-call visibility to CLI clients during Ask execution.
//
// ConciergeSession.Ask runs as an exclusive handler — its state mutations are
// not visible to shared handlers until it completes. To give the CLI real-time
// visibility into which tools are executing, Ask sends fire-and-forget updates
// to ConciergeProgress (a separate Virtual Object keyed by the same session ID).
// The CLI polls ConciergeProgress.Get while waiting for Ask to return.
//
// Lifecycle:
//  1. Ask sends Clear at the start of each invocation
//  2. Ask sends Update with status="running" before each tool call
//  3. Ask sends Update with status="ok" or "error" after each tool call
//  4. CLI polls Get, rendering new entries as they appear
//  5. Ask returns, CLI stops polling and renders the full response
package concierge

import (
	restate "github.com/restatedev/sdk-go"
)

const ConciergeProgressServiceName = "ConciergeProgress"

// ConciergeProgress is a Restate Virtual Object keyed by session ID. It
// accumulates tool execution progress entries written by ConciergeSession.Ask
// and exposes them to CLI clients via a shared (concurrent) Get handler.
type ConciergeProgress struct{}

func (ConciergeProgress) ServiceName() string { return ConciergeProgressServiceName }

// ToolProgressEntry records a single progress event for a tool call.
// Each tool generates two entries: one with status="running" when it starts,
// and one with status="ok" or "error" when it finishes.
type ToolProgressEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "running", "ok", "error"
	Error  string `json:"error,omitempty"`
}

// ProgressState holds the accumulated progress entries for the current Ask.
type ProgressState struct {
	Entries []ToolProgressEntry `json:"entries"`
}

// Update appends a tool progress entry. Called via ObjectSend (fire-and-forget)
// from ConciergeSession.Ask as each tool starts and finishes.
func (ConciergeProgress) Update(ctx restate.ObjectContext, entry ToolProgressEntry) (restate.Void, error) {
	statePtr, err := restate.Get[*ProgressState](ctx, "progress")
	if err != nil {
		return restate.Void{}, err
	}
	state := &ProgressState{}
	if statePtr != nil {
		state = statePtr
	}
	state.Entries = append(state.Entries, entry)
	restate.Set(ctx, "progress", state)
	return restate.Void{}, nil
}

// Get returns the current progress entries. This is a shared handler —
// it runs concurrently with the exclusive Update handler and reads the
// latest committed state. The CLI polls this while waiting for Ask to complete.
func (ConciergeProgress) Get(ctx restate.ObjectSharedContext) (ProgressState, error) {
	statePtr, err := restate.Get[*ProgressState](ctx, "progress")
	if err != nil {
		return ProgressState{}, err
	}
	if statePtr == nil {
		return ProgressState{}, nil
	}
	return *statePtr, nil
}

// Clear resets the progress state. Called via ObjectSend at the start of
// each Ask invocation to clear stale entries from the previous ask.
func (ConciergeProgress) Clear(ctx restate.ObjectContext, _ restate.Void) (restate.Void, error) {
	restate.Clear(ctx, "progress")
	return restate.Void{}, nil
}
