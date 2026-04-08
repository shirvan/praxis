package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSetCondition_Insert(t *testing.T) {
	conditions := []Condition{}
	result := SetCondition(conditions, Condition{Type: ConditionReady, Status: ConditionTrue}, time.Now())
	assert.Len(t, result, 1)
	assert.Equal(t, ConditionReady, result[0].Type)
	assert.Equal(t, ConditionTrue, result[0].Status)
}

func TestSetCondition_Update(t *testing.T) {
	original := time.Now().Add(-1 * time.Hour)
	conditions := []Condition{
		{Type: ConditionReady, Status: ConditionFalse, Reason: "OldReason", LastTransitionTime: original},
	}
	result := SetCondition(conditions, Condition{Type: ConditionReady, Status: ConditionTrue, Reason: "NewReason"}, time.Now())
	assert.Len(t, result, 1)
	assert.Equal(t, ConditionTrue, result[0].Status)
	assert.Equal(t, "NewReason", result[0].Reason)
	// LastTransitionTime should be updated because status changed.
	assert.True(t, result[0].LastTransitionTime.After(original))
}

func TestSetCondition_NoTransition_PreservesTimestamp(t *testing.T) {
	original := time.Now().Add(-1 * time.Hour)
	conditions := []Condition{
		{Type: ConditionReady, Status: ConditionTrue, Reason: "Same", Message: "", LastTransitionTime: original},
	}
	result := SetCondition(conditions, Condition{Type: ConditionReady, Status: ConditionTrue, Reason: "Same"}, time.Now())
	assert.Equal(t, original, result[0].LastTransitionTime)
}

func TestGetCondition_Found(t *testing.T) {
	conditions := []Condition{
		{Type: ConditionReady, Status: ConditionTrue},
		{Type: ConditionHealthy, Status: ConditionFalse},
	}
	c, ok := GetCondition(conditions, ConditionHealthy)
	assert.True(t, ok)
	assert.Equal(t, ConditionFalse, c.Status)
}

func TestGetCondition_NotFound(t *testing.T) {
	_, ok := GetCondition(nil, ConditionReady)
	assert.False(t, ok)
}

func TestIsConditionTrue_True(t *testing.T) {
	conditions := []Condition{{Type: ConditionReady, Status: ConditionTrue}}
	assert.True(t, IsConditionTrue(conditions, ConditionReady))
}

func TestIsConditionTrue_False(t *testing.T) {
	conditions := []Condition{{Type: ConditionReady, Status: ConditionFalse}}
	assert.False(t, IsConditionTrue(conditions, ConditionReady))
}

func TestIsConditionTrue_Missing(t *testing.T) {
	assert.False(t, IsConditionTrue(nil, ConditionReady))
}

func TestSetCondition_DoesNotMutateInput(t *testing.T) {
	original := []Condition{{Type: ConditionReady, Status: ConditionFalse, Reason: "Old", LastTransitionTime: time.Now()}}
	result := SetCondition(original, Condition{Type: ConditionReady, Status: ConditionTrue, Reason: "New"}, time.Now())
	assert.Equal(t, ConditionFalse, original[0].Status) // original unchanged
	assert.Equal(t, ConditionTrue, result[0].Status)
}

func TestSetCondition_MultipleTypes(t *testing.T) {
	now := time.Now()
	conditions := []Condition{}
	conditions = SetCondition(conditions, Condition{Type: ConditionReady, Status: ConditionTrue}, now)
	conditions = SetCondition(conditions, Condition{Type: ConditionHealthy, Status: ConditionTrue}, now)
	conditions = SetCondition(conditions, Condition{Type: ConditionDriftFree, Status: ConditionFalse, Reason: ReasonDriftDetected}, now)

	assert.Len(t, conditions, 3)
	assert.True(t, IsConditionTrue(conditions, ConditionReady))
	assert.True(t, IsConditionTrue(conditions, ConditionHealthy))
	assert.False(t, IsConditionTrue(conditions, ConditionDriftFree))
}

func TestSetCondition_DefaultsTimestamp(t *testing.T) {
	now := time.Now()
	result := SetCondition(nil, Condition{Type: ConditionReady, Status: ConditionTrue}, now)
	assert.False(t, result[0].LastTransitionTime.IsZero())
}
