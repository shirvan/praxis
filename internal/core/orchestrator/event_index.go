package orchestrator

import (
	"fmt"
	"sort"

	restate "github.com/restatedev/sdk-go"
)

type EventIndex struct{}

func (EventIndex) ServiceName() string {
	return EventIndexServiceName
}

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

func (EventIndex) QueryByDeployment(ctx restate.ObjectSharedContext, deploymentKey string) ([]SequencedCloudEvent, error) {
	if deploymentKey == "" {
		return nil, restate.TerminalError(fmt.Errorf("deployment key is required"), 400)
	}
	return EventIndex{}.Query(ctx, EventQuery{DeploymentKey: deploymentKey})
}

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

func sequenceRangeContains(ranges []EventSequenceRange, sequence int64) bool {
	for _, eventRange := range ranges {
		if sequence >= eventRange.StartSequence && sequence <= eventRange.EndSequence {
			return true
		}
	}
	return false
}
