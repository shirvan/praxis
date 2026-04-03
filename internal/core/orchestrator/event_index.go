// event_index.go implements the global EventIndex Restate Virtual Object.
//
// The EventIndex stores denormalised event records across all deployments under
// a single well-known key ("global"). This enables cross-deployment queries
// (e.g. "show all errors in workspace prod") without scanning every deployment's
// event store individually.
//
// The index is updated asynchronously: the EventBus sends a one-way message to
// Index() after each event is appended to the per-deployment store.
package orchestrator

import (
	"fmt"
	"sort"

	restate "github.com/restatedev/sdk-go"
)

// EventIndex is the cross-deployment event query index, stored under the
// fixed key EventIndexGlobalKey.
type EventIndex struct{}

// ServiceName returns the Restate service name for the event index.
func (EventIndex) ServiceName() string {
	return EventIndexServiceName
}

// Index adds a sequenced event to the global index. Duplicate detection is
// based on (DeploymentKey, Sequence) pairs to handle potential re-delivery.
func (EventIndex) Index(ctx restate.ObjectContext, record SequencedCloudEvent) error {
	state, err := restate.Get[[]indexedEvent](ctx, "events")
	if err != nil {
		return err
	}
	recordDeploymentKey := eventStringExtension(record.Event, EventExtensionDeployment)
	for i := range state {
		if state[i].DeploymentKey == recordDeploymentKey && state[i].Sequence == record.Sequence {
			return nil
		}
	}
	state = append(state, indexedEvent{
		DeploymentKey: recordDeploymentKey,
		Workspace:     eventStringExtension(record.Event, EventExtensionWorkspace),
		Sequence:      record.Sequence,
		Type:          record.Event.Type(),
		Severity:      eventStringExtension(record.Event, EventExtensionSeverity),
		Subject:       record.Event.Subject(),
		Time:          record.Event.Time(),
		Record:        record,
	})
	restate.Set(ctx, "events", state)
	return nil
}

// Query returns events matching the given criteria. All non-zero fields in
// the query are AND-matched. Results are returned in insertion order, limited
// to the most recent N events when query.Limit > 0.
func (EventIndex) Query(ctx restate.ObjectSharedContext, query EventQuery) ([]SequencedCloudEvent, error) {
	state, err := restate.Get[[]indexedEvent](ctx, "events")
	if err != nil {
		return nil, err
	}
	if len(state) == 0 {
		return nil, nil
	}
	out := make([]SequencedCloudEvent, 0, len(state))
	for i := range state {
		if matchesEventQuery(state[i].Record, query) {
			out = append(out, state[i].Record)
		}
	}
	return limitCloudEvents(out, query.Limit), nil
}

// QueryByDeployment is a convenience wrapper that queries events for a single
// deployment key.
func (EventIndex) QueryByDeployment(ctx restate.ObjectSharedContext, deploymentKey string) ([]SequencedCloudEvent, error) {
	if deploymentKey == "" {
		return nil, restate.TerminalError(fmt.Errorf("deployment key is required"), 400)
	}
	return EventIndex{}.Query(ctx, EventQuery{DeploymentKey: deploymentKey})
}

// Prune removes stale entries from the global index. Entries are removed if:
//   - They belong to the specified workspace and are older than req.Before.
//   - Their (DeploymentKey, Sequence) falls within a removed store range.
//   - After the above filters, the remaining count exceeds req.MaxEntries
//     (oldest are trimmed first).
//
// This is called by the retention sweep after pruning per-deployment stores.
func (EventIndex) Prune(ctx restate.ObjectContext, req EventIndexPruneRequest) (EventIndexPruneResult, error) {
	state, err := restate.Get[[]indexedEvent](ctx, "events")
	if err != nil {
		return EventIndexPruneResult{}, err
	}
	if len(state) == 0 {
		return EventIndexPruneResult{}, nil
	}
	rangeMatchers := make(map[string][]EventSequenceRange)
	for _, removed := range req.RemovedRanges {
		rangeMatchers[removed.DeploymentKey] = append(rangeMatchers[removed.DeploymentKey], removed)
	}

	unmatchedWorkspace := make([]indexedEvent, 0, len(state))
	matchedWorkspace := make([]indexedEvent, 0, len(state))
	removed := 0
	for i := range state {
		if req.Workspace != "" && state[i].Workspace != req.Workspace {
			unmatchedWorkspace = append(unmatchedWorkspace, state[i])
			continue
		}
		if !req.Before.IsZero() && state[i].Time.Before(req.Before) {
			removed++
			continue
		}
		if sequenceRangeContains(rangeMatchers[state[i].DeploymentKey], state[i].Sequence) {
			removed++
			continue
		}
		matchedWorkspace = append(matchedWorkspace, state[i])
	}
	if req.MaxEntries > 0 && len(matchedWorkspace) > req.MaxEntries {
		sort.Slice(matchedWorkspace, func(i, j int) bool {
			if matchedWorkspace[i].Time.Equal(matchedWorkspace[j].Time) {
				if matchedWorkspace[i].DeploymentKey == matchedWorkspace[j].DeploymentKey {
					return matchedWorkspace[i].Sequence < matchedWorkspace[j].Sequence
				}
				return matchedWorkspace[i].DeploymentKey < matchedWorkspace[j].DeploymentKey
			}
			return matchedWorkspace[i].Time.Before(matchedWorkspace[j].Time)
		})
		removed += len(matchedWorkspace) - req.MaxEntries
		matchedWorkspace = matchedWorkspace[len(matchedWorkspace)-req.MaxEntries:]
	}
	kept := make([]indexedEvent, 0, len(unmatchedWorkspace)+len(matchedWorkspace))
	kept = append(kept, unmatchedWorkspace...)
	kept = append(kept, matchedWorkspace...)
	restate.Set(ctx, "events", kept)
	return EventIndexPruneResult{Removed: removed, Remaining: len(kept)}, nil
}

// sequenceRangeContains checks whether a sequence number falls within any
// of the given ranges. Used during index pruning to remove entries whose
// backing store data has been deleted.
func sequenceRangeContains(ranges []EventSequenceRange, sequence int64) bool {
	for _, eventRange := range ranges {
		if sequence >= eventRange.StartSequence && sequence <= eventRange.EndSequence {
			return true
		}
	}
	return false
}
