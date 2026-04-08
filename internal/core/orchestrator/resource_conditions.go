package orchestrator

import (
	"time"

	"github.com/shirvan/praxis/pkg/types"
)

func dispatchedConditions(existing []types.Condition, now time.Time, message string) []types.Condition {
	conditions := types.SetCondition(existing, types.Condition{
		Type:    types.ConditionProvisioned,
		Status:  types.ConditionTrue,
		Reason:  types.ReasonDispatched,
		Message: message,
	}, now)
	return types.SetCondition(conditions, types.Condition{
		Type:    types.ConditionReady,
		Status:  types.ConditionUnknown,
		Reason:  types.ReasonDispatched,
		Message: message,
	}, now)
}

func readyConditions(existing []types.Condition, now time.Time, message string) []types.Condition {
	conditions := types.SetCondition(existing, types.Condition{
		Type:    types.ConditionProvisioned,
		Status:  types.ConditionTrue,
		Reason:  types.ReasonSucceeded,
		Message: message,
	}, now)
	return types.SetCondition(conditions, types.Condition{
		Type:    types.ConditionReady,
		Status:  types.ConditionTrue,
		Reason:  types.ReasonSucceeded,
		Message: message,
	}, now)
}

func retryingConditions(existing []types.Condition, now time.Time, message string) []types.Condition {
	conditions := types.SetCondition(existing, types.Condition{
		Type:    types.ConditionProvisioned,
		Status:  types.ConditionTrue,
		Reason:  types.ReasonRetrying,
		Message: message,
	}, now)
	return types.SetCondition(conditions, types.Condition{
		Type:    types.ConditionReady,
		Status:  types.ConditionFalse,
		Reason:  types.ReasonRetrying,
		Message: message,
	}, now)
}

func failedConditions(existing []types.Condition, now time.Time, reason, message string) []types.Condition {
	conditions := types.SetCondition(existing, types.Condition{
		Type:    types.ConditionProvisioned,
		Status:  types.ConditionTrue,
		Reason:  reason,
		Message: message,
	}, now)
	return types.SetCondition(conditions, types.Condition{
		Type:    types.ConditionReady,
		Status:  types.ConditionFalse,
		Reason:  reason,
		Message: message,
	}, now)
}

func skippedConditions(existing []types.Condition, now time.Time, message string) []types.Condition {
	conditions := types.SetCondition(existing, types.Condition{
		Type:    types.ConditionProvisioned,
		Status:  types.ConditionFalse,
		Reason:  types.ReasonSkipped,
		Message: message,
	}, now)
	return types.SetCondition(conditions, types.Condition{
		Type:    types.ConditionReady,
		Status:  types.ConditionFalse,
		Reason:  types.ReasonSkipped,
		Message: message,
	}, now)
}

func deletingConditions(existing []types.Condition, now time.Time, message string) []types.Condition {
	return types.SetCondition(existing, types.Condition{
		Type:    types.ConditionReady,
		Status:  types.ConditionFalse,
		Reason:  types.ReasonDeleting,
		Message: message,
	}, now)
}

func deletedConditions(existing []types.Condition, now time.Time, message string) []types.Condition {
	return types.SetCondition(existing, types.Condition{
		Type:    types.ConditionReady,
		Status:  types.ConditionFalse,
		Reason:  types.ReasonDeleting,
		Message: message,
	}, now)
}

func orphanedConditions(existing []types.Condition, now time.Time, message string) []types.Condition {
	return types.SetCondition(existing, types.Condition{
		Type:    types.ConditionReady,
		Status:  types.ConditionFalse,
		Reason:  types.ReasonOrphaned,
		Message: message,
	}, now)
}
