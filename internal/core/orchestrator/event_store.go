package orchestrator

import (
	"fmt"
	"sort"
	"strings"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	restate "github.com/restatedev/sdk-go"
)

type DeploymentEventStore struct{}

func (DeploymentEventStore) ServiceName() string {
	return DeploymentEventStoreServiceName
}

func (DeploymentEventStore) Append(ctx restate.ObjectContext, event cloudevents.Event) (SequencedCloudEvent, error) {
	if event.SpecVersion() == "" {
		event.SetSpecVersion(cloudevents.VersionV1)
	}
	if event.Time().IsZero() {
		now, err := currentTime(ctx)
		if err != nil {
			return SequencedCloudEvent{}, err
		}
		event.SetTime(now)
	}
	if event.DataContentType() == "" && len(event.Data()) > 0 {
		event.SetDataContentType(cloudevents.ApplicationJSON)
	}

	meta, err := restate.Get[*eventStoreMeta](ctx, "meta")
	if err != nil {
		return SequencedCloudEvent{}, err
	}
	if meta == nil {
		meta = &eventStoreMeta{ChunkSize: defaultEventChunkSize}
	}
	if meta.ChunkSize <= 0 {
		meta.ChunkSize = defaultEventChunkSize
	}
	if meta.ActiveCount == 0 && meta.NextSequence > 0 {
		meta.ActiveCount, err = activeEventCountObject(ctx, meta)
		if err != nil {
			return SequencedCloudEvent{}, err
		}
	}

	chunkIndex := meta.ChunkCount
	if chunkIndex == 0 {
		chunkIndex = 1
		meta.ChunkCount = 1
	}

	chunkKey := eventChunkKey(chunkIndex)
	chunk, err := restate.Get[[]SequencedCloudEvent](ctx, chunkKey)
	if err != nil {
		return SequencedCloudEvent{}, err
	}
	if len(chunk) >= meta.ChunkSize {
		chunkIndex++
		meta.ChunkCount = chunkIndex
		chunkKey = eventChunkKey(chunkIndex)
		chunk = nil
	}

	meta.NextSequence++
	meta.ActiveCount++
	event.SetID(fmt.Sprintf("%s-%d", restate.Key(ctx), meta.NextSequence))
	if err := event.Validate(); err != nil {
		return SequencedCloudEvent{}, restate.TerminalError(fmt.Errorf("invalid CloudEvent: %w", err), 400)
	}
	record := SequencedCloudEvent{Sequence: meta.NextSequence, Event: event}
	chunk = append(chunk, record)

	restate.Set(ctx, chunkKey, chunk)
	restate.Set(ctx, "meta", meta)
	return record, nil
}

func (DeploymentEventStore) ListSince(ctx restate.ObjectSharedContext, seq int64) ([]SequencedCloudEvent, error) {
	if seq < 0 {
		return nil, restate.TerminalError(fmt.Errorf("sequence must be >= 0"), 400)
	}
	meta, err := restate.Get[*eventStoreMeta](ctx, "meta")
	if err != nil {
		return nil, err
	}
	if meta == nil || meta.NextSequence == 0 {
		return nil, nil
	}

	out := make([]SequencedCloudEvent, 0)
	for index := 1; index <= meta.ChunkCount; index++ {
		chunk, err := restate.Get[[]SequencedCloudEvent](ctx, eventChunkKey(index))
		if err != nil {
			return nil, err
		}
		for _, record := range chunk {
			if record.Sequence > seq {
				out = append(out, record)
			}
		}
	}
	return out, nil
}

func (DeploymentEventStore) ListByType(ctx restate.ObjectSharedContext, prefix string) ([]SequencedCloudEvent, error) {
	events, err := DeploymentEventStore{}.ListSince(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]SequencedCloudEvent, 0, len(events))
	for _, record := range events {
		if prefix == "" || strings.HasPrefix(record.Event.Type(), prefix) {
			out = append(out, record)
		}
	}
	return out, nil
}

func (DeploymentEventStore) ListByResource(ctx restate.ObjectSharedContext, subject string) ([]SequencedCloudEvent, error) {
	events, err := DeploymentEventStore{}.ListSince(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]SequencedCloudEvent, 0, len(events))
	for _, record := range events {
		if subject == "" || record.Event.Subject() == subject {
			out = append(out, record)
		}
	}
	return out, nil
}

func (DeploymentEventStore) GetRange(ctx restate.ObjectSharedContext, req EventRangeRequest) ([]SequencedCloudEvent, error) {
	if req.StartSequence < 0 || req.EndSequence < 0 {
		return nil, restate.TerminalError(fmt.Errorf("sequence range must be >= 0"), 400)
	}
	if req.EndSequence > 0 && req.EndSequence < req.StartSequence {
		return nil, restate.TerminalError(fmt.Errorf("end sequence must be >= start sequence"), 400)
	}
	events, err := DeploymentEventStore{}.ListSince(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]SequencedCloudEvent, 0, len(events))
	for _, record := range events {
		if record.Sequence < req.StartSequence {
			continue
		}
		if req.EndSequence > 0 && record.Sequence > req.EndSequence {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

func (DeploymentEventStore) Count(ctx restate.ObjectSharedContext) (int64, error) {
	meta, err := restate.Get[*eventStoreMeta](ctx, "meta")
	if err != nil {
		return 0, err
	}
	if meta == nil {
		return 0, nil
	}
	if meta.ActiveCount > 0 || meta.NextSequence == 0 {
		return meta.ActiveCount, nil
	}
	return activeEventCountShared(ctx, meta)
}

func (DeploymentEventStore) RollbackPlan(ctx restate.ObjectSharedContext, _ restate.Void) (RollbackPlan, error) {
	records, err := DeploymentEventStore{}.ListByType(ctx, EventTypeResourceReady)
	if err != nil {
		return RollbackPlan{}, err
	}
	latestByResource := make(map[string]RollbackResource, len(records))
	for _, record := range records {
		if record.Event.Type() != EventTypeResourceReady {
			continue
		}
		name := strings.TrimSpace(record.Event.Subject())
		if name == "" {
			name = strings.TrimSpace(eventDataString(record.Event, "resourceName"))
		}
		if name == "" {
			continue
		}
		payload := eventData(record.Event)
		_ = payload
		latestByResource[name] = RollbackResource{
			Sequence: record.Sequence,
			Name:     name,
			Kind:     strings.TrimSpace(eventStringExtension(record.Event, EventExtensionResourceKind)),
		}
	}
	resources := make([]RollbackResource, 0, len(latestByResource))
	for _, resource := range latestByResource {
		resources = append(resources, resource)
	}
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].Sequence == resources[j].Sequence {
			return resources[i].Name < resources[j].Name
		}
		return resources[i].Sequence > resources[j].Sequence
	})
	return RollbackPlan{DeploymentKey: restate.Key(ctx), Resources: resources}, nil
}

func (DeploymentEventStore) Prune(ctx restate.ObjectContext, req EventStorePruneRequest) (EventStorePruneResult, error) {
	meta, err := restate.Get[*eventStoreMeta](ctx, "meta")
	if err != nil {
		return EventStorePruneResult{}, err
	}
	if meta == nil || meta.NextSequence == 0 {
		return EventStorePruneResult{DeploymentKey: restate.Key(ctx)}, nil
	}
	if meta.ChunkSize <= 0 {
		meta.ChunkSize = defaultEventChunkSize
	}
	if meta.ActiveCount == 0 && meta.NextSequence > 0 {
		meta.ActiveCount, err = activeEventCountObject(ctx, meta)
		if err != nil {
			return EventStorePruneResult{}, err
		}
	}
	if req.MaxEvents < 0 {
		return EventStorePruneResult{}, restate.TerminalError(fmt.Errorf("maxEvents must be >= 0"), 400)
	}
	if req.ShipBeforeDelete && strings.TrimSpace(req.DrainSink) == "" {
		return EventStorePruneResult{}, restate.TerminalError(fmt.Errorf("drain sink is required when shipBeforeDelete is true"), 400)
	}
	batchSize := req.BatchSize
	if batchSize <= 0 {
		batchSize = defaultEventChunkSize
	}

	type chunkInfo struct {
		key     string
		records []SequencedCloudEvent
	}
	chunks := make([]chunkInfo, 0, meta.ChunkCount)
	for index := 1; index <= meta.ChunkCount; index++ {
		chunkKey := eventChunkKey(index)
		chunk, chunkErr := restate.Get[[]SequencedCloudEvent](ctx, chunkKey)
		if chunkErr != nil {
			return EventStorePruneResult{}, chunkErr
		}
		if len(chunk) == 0 {
			continue
		}
		chunks = append(chunks, chunkInfo{key: chunkKey, records: chunk})
	}
	if len(chunks) == 0 {
		meta.ActiveCount = 0
		restate.Set(ctx, "meta", meta)
		return EventStorePruneResult{DeploymentKey: restate.Key(ctx)}, nil
	}

	remaining := meta.ActiveCount
	prunedChunks := make([]chunkInfo, 0)
	prunedRecords := make([]SequencedCloudEvent, 0)
	removedRanges := make([]EventSequenceRange, 0)
	for _, chunk := range chunks {
		eligibleByAge := !req.Before.IsZero() && chunk.records[len(chunk.records)-1].Event.Time().Before(req.Before)
		eligibleByCount := req.MaxEvents > 0 && remaining > int64(req.MaxEvents)
		if !eligibleByAge && !eligibleByCount {
			continue
		}
		prunedChunks = append(prunedChunks, chunk)
		prunedRecords = append(prunedRecords, chunk.records...)
		removedRanges = append(removedRanges, EventSequenceRange{
			DeploymentKey: restate.Key(ctx),
			StartSequence: chunk.records[0].Sequence,
			EndSequence:   chunk.records[len(chunk.records)-1].Sequence,
		})
		remaining -= int64(len(chunk.records))
	}

	if len(prunedChunks) == 0 {
		return EventStorePruneResult{DeploymentKey: restate.Key(ctx), RemainingEvents: remaining}, nil
	}

	shippedEvents := 0
	if req.ShipBeforeDelete {
		for start := 0; start < len(prunedRecords); start += batchSize {
			end := min(start+batchSize, len(prunedRecords))
			if _, drainErr := restate.WithRequestType[DrainBatchRequest, restate.Void](
				restate.Service[restate.Void](ctx, SinkRouterServiceName, "DrainBatch"),
			).Request(DrainBatchRequest{SinkName: req.DrainSink, Records: prunedRecords[start:end]}); drainErr != nil {
				return EventStorePruneResult{}, drainErr
			}
			shippedEvents += end - start
		}
	}

	for _, chunk := range prunedChunks {
		restate.Clear(ctx, chunk.key)
	}
	if remaining < 0 {
		remaining = 0
	}
	meta.ActiveCount = remaining
	restate.Set(ctx, "meta", meta)

	return EventStorePruneResult{
		DeploymentKey:   restate.Key(ctx),
		PrunedEvents:    len(prunedRecords),
		PrunedChunks:    len(prunedChunks),
		RemainingEvents: remaining,
		ShippedEvents:   shippedEvents,
		RemovedRanges:   removedRanges,
	}, nil
}

func activeEventCountObject(ctx restate.ObjectContext, meta *eventStoreMeta) (int64, error) {
	var count int64
	for index := 1; index <= meta.ChunkCount; index++ {
		chunk, err := restate.Get[[]SequencedCloudEvent](ctx, eventChunkKey(index))
		if err != nil {
			return 0, err
		}
		count += int64(len(chunk))
	}
	return count, nil
}

func activeEventCountShared(ctx restate.ObjectSharedContext, meta *eventStoreMeta) (int64, error) {
	var count int64
	for index := 1; index <= meta.ChunkCount; index++ {
		chunk, err := restate.Get[[]SequencedCloudEvent](ctx, eventChunkKey(index))
		if err != nil {
			return 0, err
		}
		count += int64(len(chunk))
	}
	return count, nil
}
