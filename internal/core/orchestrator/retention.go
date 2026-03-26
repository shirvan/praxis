package orchestrator

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

const (
	retentionScheduleStateKey = "retentionSchedules"
	retentionSystemStreamBase = "__system__/retention/"
	workspaceServiceName      = "WorkspaceService"
)

type retentionPolicySnapshot struct {
	MaxAge                 string `json:"maxAge,omitempty"`
	MaxEventsPerDeployment int    `json:"maxEventsPerDeployment,omitempty"`
	MaxIndexEntries        int    `json:"maxIndexEntries,omitempty"`
	SweepInterval          string `json:"sweepInterval,omitempty"`
	ShipBeforeDelete       bool   `json:"shipBeforeDelete,omitempty"`
	DrainSink              string `json:"drainSink,omitempty"`
}

func (b EventBus) ScheduleRetentionSweep(ctx restate.ObjectContext, req RetentionSweepRequest) error {
	workspace := strings.TrimSpace(req.Workspace)
	if workspace == "" {
		return restate.TerminalError(fmt.Errorf("workspace is required"), 400)
	}
	policy, err := loadRetentionPolicy(ctx, workspace)
	if err != nil {
		return err
	}
	delay, err := parseRetentionDuration(policy.SweepInterval)
	if err != nil {
		return restate.TerminalError(fmt.Errorf("invalid sweep interval %q: %w", policy.SweepInterval, err), 400)
	}
	scheduled, err := restate.Get[map[string]bool](ctx, retentionScheduleStateKey)
	if err != nil {
		return err
	}
	if scheduled == nil {
		scheduled = make(map[string]bool)
	}
	if scheduled[workspace] {
		return nil
	}
	restate.ObjectSend(ctx, EventBusServiceName, EventBusGlobalKey, "RunRetentionSweep").
		Send(RetentionSweepRequest{Workspace: workspace}, restate.WithDelay(delay))
	scheduled[workspace] = true
	restate.Set(ctx, retentionScheduleStateKey, scheduled)
	return nil
}

func (b EventBus) RunRetentionSweep(ctx restate.ObjectContext, req RetentionSweepRequest) (RetentionSweepResult, error) {
	workspace := strings.TrimSpace(req.Workspace)
	if workspace == "" {
		return RetentionSweepResult{}, restate.TerminalError(fmt.Errorf("workspace is required"), 400)
	}
	if err := setRetentionScheduled(ctx, workspace, false); err != nil {
		return RetentionSweepResult{}, err
	}

	policy, err := loadRetentionPolicy(ctx, workspace)
	if err != nil {
		return RetentionSweepResult{Workspace: workspace}, err
	}
	now, err := currentTime(ctx)
	if err != nil {
		return RetentionSweepResult{Workspace: workspace}, err
	}
	before, err := retentionCutoff(now, policy.MaxAge)
	if err != nil {
		return RetentionSweepResult{Workspace: workspace}, restate.TerminalError(fmt.Errorf("invalid maxAge %q: %w", policy.MaxAge, err), 400)
	}

	_ = b.emitWorkspaceRetentionEvent(ctx, workspace, EventTypeSystemRetentionSweepStarted, EventSeverityInfo, map[string]any{
		"message":   "retention sweep started",
		"workspace": workspace,
		"maxAge":    policy.MaxAge,
	})

	summaries, err := restate.WithRequestType[ListFilter, []types.DeploymentSummary](
		restate.Object[[]types.DeploymentSummary](ctx, DeploymentIndexServiceName, DeploymentIndexGlobalKey, "List"),
	).Request(ListFilter{Workspace: workspace})
	if err != nil {
		return RetentionSweepResult{Workspace: workspace}, err
	}

	result := RetentionSweepResult{
		Workspace:          workspace,
		DeploymentsScanned: len(summaries),
	}
	removedRanges := make([]EventSequenceRange, 0)
	for _, summary := range summaries {
		pruneResult, pruneErr := restate.WithRequestType[EventStorePruneRequest, EventStorePruneResult](
			restate.Object[EventStorePruneResult](ctx, DeploymentEventStoreServiceName, summary.Key, "Prune"),
		).Request(EventStorePruneRequest{
			Before:           before,
			MaxEvents:        policy.MaxEventsPerDeployment,
			ShipBeforeDelete: policy.ShipBeforeDelete,
			DrainSink:        policy.DrainSink,
			BatchSize:        defaultEventChunkSize,
		})
		if pruneErr != nil {
			result.FailedDeployments = append(result.FailedDeployments, summary.Key)
			_ = b.emitDeploymentRetentionEvent(ctx, workspace, summary.Key, EventTypeSystemRetentionShipFailed, EventSeverityError, map[string]any{
				"message":       "retention shipping failed",
				"deploymentKey": summary.Key,
				"error":         pruneErr.Error(),
			})
			continue
		}
		if pruneResult.PrunedEvents > 0 {
			result.DeploymentsPruned++
		}
		result.PrunedEvents += pruneResult.PrunedEvents
		result.PrunedChunks += pruneResult.PrunedChunks
		result.ShippedEvents += pruneResult.ShippedEvents
		removedRanges = append(removedRanges, pruneResult.RemovedRanges...)
		if pruneResult.ShippedEvents > 0 {
			_ = b.emitDeploymentRetentionEvent(ctx, workspace, summary.Key, EventTypeSystemRetentionShipCompleted, EventSeverityInfo, map[string]any{
				"message":       "retention shipping completed",
				"deploymentKey": summary.Key,
				"shippedEvents": pruneResult.ShippedEvents,
				"prunedChunks":  pruneResult.PrunedChunks,
			})
		}
	}

	indexResult, err := restate.WithRequestType[EventIndexPruneRequest, EventIndexPruneResult](
		restate.Object[EventIndexPruneResult](ctx, EventIndexServiceName, EventIndexGlobalKey, "Prune"),
	).Request(EventIndexPruneRequest{
		Workspace:     workspace,
		Before:        before,
		MaxEntries:    policy.MaxIndexEntries,
		RemovedRanges: removedRanges,
	})
	if err != nil {
		return result, err
	}
	result.IndexEntriesPruned = indexResult.Removed

	_ = b.emitWorkspaceRetentionEvent(ctx, workspace, EventTypeSystemRetentionSweepCompleted, EventSeverityInfo, map[string]any{
		"message":            "retention sweep completed",
		"workspace":          workspace,
		"deploymentsScanned": result.DeploymentsScanned,
		"deploymentsPruned":  result.DeploymentsPruned,
		"prunedEvents":       result.PrunedEvents,
		"prunedChunks":       result.PrunedChunks,
		"shippedEvents":      result.ShippedEvents,
		"indexEntriesPruned": result.IndexEntriesPruned,
		"failedDeployments":  result.FailedDeployments,
	})

	if scheduleErr := b.ScheduleRetentionSweep(ctx, RetentionSweepRequest{Workspace: workspace}); scheduleErr != nil {
		return result, scheduleErr
	}
	return result, nil
}

func setRetentionScheduled(ctx restate.ObjectContext, workspace string, scheduledValue bool) error {
	scheduled, err := restate.Get[map[string]bool](ctx, retentionScheduleStateKey)
	if err != nil {
		return err
	}
	if scheduled == nil {
		scheduled = make(map[string]bool)
	}
	if scheduledValue {
		scheduled[workspace] = true
	} else {
		delete(scheduled, workspace)
	}
	restate.Set(ctx, retentionScheduleStateKey, scheduled)
	return nil
}

func loadRetentionPolicy(ctx restate.Context, workspace string) (retentionPolicySnapshot, error) {
	return restate.Object[retentionPolicySnapshot](ctx, workspaceServiceName, workspace, "GetEventRetention").Request(restate.Void{})
}

func parseRetentionDuration(raw string) (time.Duration, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("duration is required")
	}
	if days, ok := strings.CutSuffix(value, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	if hours, ok := strings.CutSuffix(value, "h"); ok {
		n, err := strconv.Atoi(hours)
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * time.Hour, nil
	}
	return time.ParseDuration(value)
}

func retentionCutoff(now time.Time, maxAge string) (time.Time, error) {
	duration, err := parseRetentionDuration(maxAge)
	if err != nil {
		return time.Time{}, err
	}
	return now.Add(-duration), nil
}

func (b EventBus) emitWorkspaceRetentionEvent(ctx restate.ObjectContext, workspace, eventType, severity string, data map[string]any) error {
	event, err := newRetentionSystemEvent(retentionSystemStreamBase+workspace, workspace, eventType, severity, "", data)
	if err != nil {
		return err
	}
	return b.Emit(ctx, event)
}

func (b EventBus) emitDeploymentRetentionEvent(ctx restate.ObjectContext, workspace, deploymentKey, eventType, severity string, data map[string]any) error {
	event, err := newRetentionSystemEvent(deploymentKey, workspace, eventType, severity, deploymentKey, data)
	if err != nil {
		return err
	}
	return b.Emit(ctx, event)
}

func newRetentionSystemEvent(deploymentKey, workspace, eventType, severity, subject string, data map[string]any) (cloudevents.Event, error) {
	event := cloudevents.NewEvent(cloudevents.VersionV1)
	event.SetSource(fmt.Sprintf("/praxis/%s/%s", workspace, deploymentKey))
	event.SetType(eventType)
	if subject != "" {
		event.SetSubject(subject)
	}
	event.SetExtension(EventExtensionDeployment, deploymentKey)
	event.SetExtension(EventExtensionWorkspace, workspace)
	event.SetExtension(EventExtensionGeneration, int64(0))
	event.SetExtension(EventExtensionCategory, EventCategorySystem)
	event.SetExtension(EventExtensionSeverity, severity)
	if data == nil {
		data = map[string]any{"message": eventType}
	}
	if _, ok := data["message"]; !ok {
		data["message"] = eventType
	}
	if err := event.SetData(cloudevents.ApplicationJSON, data); err != nil {
		return cloudevents.Event{}, err
	}
	return event, nil
}
