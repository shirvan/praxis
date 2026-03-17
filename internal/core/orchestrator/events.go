package orchestrator

import (
	"fmt"

	restate "github.com/restatedev/sdk-go"
)

type deploymentEventState struct {
	NextSequence int64             `json:"nextSequence"`
	Events       []DeploymentEvent `json:"events"`
}

// DeploymentEvents stores an append-only event feed for one deployment key.
//
// The event feed is intentionally simple: it is optimized for polling and audit
// readability rather than for high-volume streaming semantics.
type DeploymentEvents struct{}

func (DeploymentEvents) ServiceName() string {
	return DeploymentEventsServiceName
}

// Append stores one new event and assigns a monotonically increasing sequence.
func (DeploymentEvents) Append(ctx restate.ObjectContext, event DeploymentEvent) error {
	state, err := restate.Get[*deploymentEventState](ctx, "state")
	if err != nil {
		return err
	}
	if state == nil {
		state = &deploymentEventState{}
	}

	if event.DeploymentKey == "" {
		event.DeploymentKey = restate.Key(ctx)
	}
	now, err := currentTime(ctx)
	if err != nil {
		return err
	}
	state.NextSequence++
	event.Sequence = state.NextSequence
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	state.Events = append(state.Events, event)
	restate.Set(ctx, "state", state)
	return nil
}

// ListSince returns all events whose sequence is greater than the provided
// cursor.
func (DeploymentEvents) ListSince(ctx restate.ObjectSharedContext, seq int64) ([]DeploymentEvent, error) {
	if seq < 0 {
		return nil, restate.TerminalError(fmt.Errorf("sequence must be >= 0"), 400)
	}
	state, err := restate.Get[*deploymentEventState](ctx, "state")
	if err != nil {
		return nil, err
	}
	if state == nil || len(state.Events) == 0 {
		return nil, nil
	}

	filtered := make([]DeploymentEvent, 0)
	for _, event := range state.Events {
		if event.Sequence > seq {
			filtered = append(filtered, event)
		}
	}
	return filtered, nil
}
