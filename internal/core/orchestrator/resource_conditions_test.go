package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestDispatchedConditions(t *testing.T) {
	now := time.Now()
	conditions := dispatchedConditions(nil, now, "provision dispatched")

	assert.Len(t, conditions, 2)
	assert.True(t, types.IsConditionTrue(conditions, types.ConditionProvisioned))

	ready, ok := types.GetCondition(conditions, types.ConditionReady)
	assert.True(t, ok)
	assert.Equal(t, types.ConditionUnknown, ready.Status)
	assert.Equal(t, types.ReasonDispatched, ready.Reason)
}

func TestReadyConditions(t *testing.T) {
	now := time.Now()
	conditions := readyConditions(nil, now, "resource provisioned successfully")

	assert.Len(t, conditions, 2)
	assert.True(t, types.IsConditionTrue(conditions, types.ConditionReady))
	assert.True(t, types.IsConditionTrue(conditions, types.ConditionProvisioned))

	ready, ok := types.GetCondition(conditions, types.ConditionReady)
	assert.True(t, ok)
	assert.Equal(t, types.ReasonSucceeded, ready.Reason)
}

func TestRetryingConditions(t *testing.T) {
	now := time.Now()
	conditions := retryingConditions(nil, now, "retrying after error")

	assert.Len(t, conditions, 2)
	assert.True(t, types.IsConditionTrue(conditions, types.ConditionProvisioned))
	assert.False(t, types.IsConditionTrue(conditions, types.ConditionReady))

	ready, ok := types.GetCondition(conditions, types.ConditionReady)
	assert.True(t, ok)
	assert.Equal(t, types.ReasonRetrying, ready.Reason)
}

func TestFailedConditions(t *testing.T) {
	now := time.Now()
	conditions := failedConditions(nil, now, types.ReasonProvisionFailed, "access denied")

	assert.Len(t, conditions, 2)
	assert.False(t, types.IsConditionTrue(conditions, types.ConditionReady))

	ready, ok := types.GetCondition(conditions, types.ConditionReady)
	assert.True(t, ok)
	assert.Equal(t, types.ReasonProvisionFailed, ready.Reason)
	assert.Equal(t, "access denied", ready.Message)
}

func TestSkippedConditions(t *testing.T) {
	now := time.Now()
	conditions := skippedConditions(nil, now, "skipped due to upstream failure")

	assert.Len(t, conditions, 2)
	assert.False(t, types.IsConditionTrue(conditions, types.ConditionReady))
	assert.False(t, types.IsConditionTrue(conditions, types.ConditionProvisioned))

	ready, ok := types.GetCondition(conditions, types.ConditionReady)
	assert.True(t, ok)
	assert.Equal(t, types.ReasonSkipped, ready.Reason)
}

func TestDeletingConditions(t *testing.T) {
	now := time.Now()
	conditions := deletingConditions(nil, now, "delete in progress")

	assert.Len(t, conditions, 1)
	ready, ok := types.GetCondition(conditions, types.ConditionReady)
	assert.True(t, ok)
	assert.Equal(t, types.ConditionFalse, ready.Status)
	assert.Equal(t, types.ReasonDeleting, ready.Reason)
}

func TestOrphanedConditions(t *testing.T) {
	now := time.Now()
	conditions := orphanedConditions(nil, now, "resource orphaned from management")

	assert.Len(t, conditions, 1)
	ready, ok := types.GetCondition(conditions, types.ConditionReady)
	assert.True(t, ok)
	assert.Equal(t, types.ConditionFalse, ready.Status)
	assert.Equal(t, types.ReasonOrphaned, ready.Reason)
}

func TestConditionTransition_DispatchToReady(t *testing.T) {
	now := time.Now()
	conditions := dispatchedConditions(nil, now, "dispatched")

	later := now.Add(5 * time.Second)
	conditions = readyConditions(conditions, later, "ready")

	assert.Len(t, conditions, 2)
	assert.True(t, types.IsConditionTrue(conditions, types.ConditionReady))
	assert.True(t, types.IsConditionTrue(conditions, types.ConditionProvisioned))
}

func TestConditionTransition_DispatchToRetryToReady(t *testing.T) {
	now := time.Now()
	conditions := dispatchedConditions(nil, now, "dispatched")
	conditions = retryingConditions(conditions, now.Add(1*time.Second), "retry 1")
	conditions = readyConditions(conditions, now.Add(5*time.Second), "ready after retry")

	assert.True(t, types.IsConditionTrue(conditions, types.ConditionReady))
	assert.True(t, types.IsConditionTrue(conditions, types.ConditionProvisioned))
}

func TestConditionTransition_DispatchToFailed(t *testing.T) {
	now := time.Now()
	conditions := dispatchedConditions(nil, now, "dispatched")
	conditions = failedConditions(conditions, now.Add(1*time.Second), types.ReasonProvisionFailed, "fatal error")

	assert.False(t, types.IsConditionTrue(conditions, types.ConditionReady))
	assert.True(t, types.IsConditionTrue(conditions, types.ConditionProvisioned))
}
