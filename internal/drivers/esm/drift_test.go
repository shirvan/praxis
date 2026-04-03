package esm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------- HasDrift ----------

func TestESMHasDrift(t *testing.T) {
	batchSize := int32(10)
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true, BatchSize: &batchSize})
	observed := ObservedState{State: "Enabled", BatchSize: 5}
	assert.True(t, HasDrift(desired, observed))
}

func TestESMNoDrift(t *testing.T) {
	batchSize := int32(10)
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true, BatchSize: &batchSize})
	observed := ObservedState{State: "Enabled", BatchSize: 10}
	assert.False(t, HasDrift(desired, observed))
}

func TestESMHasDrift_EnabledToDisabled(t *testing.T) {
	desired := applyDefaults(EventSourceMappingSpec{Enabled: false})
	observed := ObservedState{State: "Enabled"}
	assert.True(t, HasDrift(desired, observed))
}

func TestESMHasDrift_DisabledToEnabled(t *testing.T) {
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true})
	observed := ObservedState{State: "Disabled"}
	assert.True(t, HasDrift(desired, observed))
}

func TestESMHasDrift_BatchSizeChanged(t *testing.T) {
	bs := int32(100)
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true, BatchSize: &bs})
	observed := ObservedState{State: "Enabled", BatchSize: 50}
	assert.True(t, HasDrift(desired, observed))
}

func TestESMHasDrift_BatchSizeNilNoChange(t *testing.T) {
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true})
	observed := ObservedState{State: "Enabled", BatchSize: 100}
	assert.False(t, HasDrift(desired, observed))
}

func TestESMHasDrift_BatchingWindowChanged(t *testing.T) {
	window := int32(30)
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true, MaximumBatchingWindowInSeconds: &window})
	observed := ObservedState{State: "Enabled", MaximumBatchingWindowInSeconds: 0}
	assert.True(t, HasDrift(desired, observed))
}

func TestESMHasDrift_BisectChanged(t *testing.T) {
	bisect := true
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true, BisectBatchOnFunctionError: &bisect})
	observed := ObservedState{State: "Enabled", BisectBatchOnFunctionError: false}
	assert.True(t, HasDrift(desired, observed))
}

func TestESMHasDrift_ParallelizationChanged(t *testing.T) {
	pf := int32(5)
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true, ParallelizationFactor: &pf})
	observed := ObservedState{State: "Enabled", ParallelizationFactor: 1}
	assert.True(t, HasDrift(desired, observed))
}

func TestESMHasDrift_TumblingWindowChanged(t *testing.T) {
	tw := int32(60)
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true, TumblingWindowInSeconds: &tw})
	observed := ObservedState{State: "Enabled", TumblingWindowInSeconds: 0}
	assert.True(t, HasDrift(desired, observed))
}

func TestESMHasDrift_RetryAttemptsChanged(t *testing.T) {
	retries := int32(3)
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true, MaximumRetryAttempts: &retries})
	observed := ObservedState{State: "Enabled", MaximumRetryAttempts: int32Ptr(10)}
	assert.True(t, HasDrift(desired, observed))
}

func TestESMHasDrift_RecordAgeChanged(t *testing.T) {
	age := int32(3600)
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true, MaximumRecordAgeInSeconds: &age})
	observed := ObservedState{State: "Enabled", MaximumRecordAgeInSeconds: int32Ptr(86400)}
	assert.True(t, HasDrift(desired, observed))
}

func TestESMHasDrift_FunctionResponseTypesChanged(t *testing.T) {
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true, FunctionResponseTypes: []string{"ReportBatchItemFailures"}})
	observed := ObservedState{State: "Enabled", FunctionResponseTypes: nil}
	assert.True(t, HasDrift(desired, observed))
}

func TestESMHasDrift_DestinationConfigChanged(t *testing.T) {
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true,
		DestinationConfig: &DestinationSpec{OnFailure: OnFailureSpec{DestinationArn: "arn:aws:sqs:us-east-1:123:dlq"}}})
	observed := ObservedState{State: "Enabled", DestinationConfig: nil}
	assert.True(t, HasDrift(desired, observed))
}

func TestESMHasDrift_ScalingConfigChanged(t *testing.T) {
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true,
		ScalingConfig: &ScalingSpec{MaximumConcurrency: 10}})
	observed := ObservedState{State: "Enabled", ScalingConfig: &ScalingSpec{MaximumConcurrency: 5}}
	assert.True(t, HasDrift(desired, observed))
}

func TestESMHasDrift_FilterCriteriaChanged(t *testing.T) {
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true,
		FilterCriteria: &FilterCriteriaSpec{Filters: []FilterSpec{{Pattern: `{"body":{"key":["value"]}}`}}}})
	observed := ObservedState{State: "Enabled", FilterCriteria: nil}
	assert.True(t, HasDrift(desired, observed))
}

// ---------- ComputeFieldDiffs ----------

func TestESMComputeFieldDiffs_NoDiffs(t *testing.T) {
	bs := int32(10)
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true, BatchSize: &bs})
	observed := ObservedState{State: "Enabled", BatchSize: 10}
	assert.Empty(t, ComputeFieldDiffs(desired, observed))
}

func TestESMComputeFieldDiffs_BatchSize(t *testing.T) {
	bs := int32(100)
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true, BatchSize: &bs})
	observed := ObservedState{State: "Enabled", BatchSize: 10}
	diffs := ComputeFieldDiffs(desired, observed)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.batchSize", diffs[0].Path)
	assert.Equal(t, int32(10), diffs[0].OldValue)
	assert.Equal(t, int32(100), diffs[0].NewValue)
}

func TestESMComputeFieldDiffs_EnabledState(t *testing.T) {
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true})
	observed := ObservedState{State: "Disabled"}
	diffs := ComputeFieldDiffs(desired, observed)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.enabled", diffs[0].Path)
}

func TestESMComputeFieldDiffs_MultipleDiffs(t *testing.T) {
	bs := int32(100)
	pf := int32(5)
	desired := applyDefaults(EventSourceMappingSpec{Enabled: false, BatchSize: &bs, ParallelizationFactor: &pf})
	observed := ObservedState{State: "Enabled", BatchSize: 10, ParallelizationFactor: 1}
	diffs := ComputeFieldDiffs(desired, observed)
	assert.Len(t, diffs, 3)
}

// ---------- Helper functions ----------

func TestFilterCriteriaMatch_BothNil(t *testing.T) {
	assert.True(t, filterCriteriaMatch(nil, nil))
}

func TestFilterCriteriaMatch_OneNil(t *testing.T) {
	assert.False(t, filterCriteriaMatch(&FilterCriteriaSpec{Filters: []FilterSpec{{Pattern: "x"}}}, nil))
	assert.False(t, filterCriteriaMatch(nil, &FilterCriteriaSpec{Filters: []FilterSpec{{Pattern: "x"}}}))
}

func TestFilterCriteriaMatch_DifferentLengths(t *testing.T) {
	a := &FilterCriteriaSpec{Filters: []FilterSpec{{Pattern: "a"}, {Pattern: "b"}}}
	b := &FilterCriteriaSpec{Filters: []FilterSpec{{Pattern: "a"}}}
	assert.False(t, filterCriteriaMatch(a, b))
}

func TestDestinationMatch_BothNil(t *testing.T) {
	assert.True(t, destinationMatch(nil, nil))
}

func TestDestinationMatch_OneNil(t *testing.T) {
	assert.False(t, destinationMatch(&DestinationSpec{OnFailure: OnFailureSpec{DestinationArn: "arn"}}, nil))
}

func TestScalingMatch_BothNil(t *testing.T) {
	assert.True(t, scalingMatch(nil, nil))
}

func TestScalingMatch_OneNil(t *testing.T) {
	assert.False(t, scalingMatch(&ScalingSpec{MaximumConcurrency: 10}, nil))
}

func TestInt32PtrMatch_BothNil(t *testing.T) {
	assert.True(t, int32PtrMatch(nil, nil))
}

func TestInt32PtrMatch_OneNil(t *testing.T) {
	v := int32(5)
	assert.False(t, int32PtrMatch(&v, nil))
	assert.False(t, int32PtrMatch(nil, &v))
}

func TestInt32PtrMatch_Equal(t *testing.T) {
	a, b := int32(5), int32(5)
	assert.True(t, int32PtrMatch(&a, &b))
}

func TestInt32PtrMatch_Different(t *testing.T) {
	a, b := int32(5), int32(10)
	assert.False(t, int32PtrMatch(&a, &b))
}
