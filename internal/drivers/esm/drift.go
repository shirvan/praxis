package esm

import "slices"

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func HasDrift(desired EventSourceMappingSpec, observed ObservedState) bool {
	return len(ComputeFieldDiffs(desired, observed)) > 0
}

func ComputeFieldDiffs(desired EventSourceMappingSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	if desired.Enabled && observed.State == "Disabled" {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.enabled", OldValue: false, NewValue: true})
	}
	if !desired.Enabled && observed.State == "Enabled" {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.enabled", OldValue: true, NewValue: false})
	}
	if desired.BatchSize != nil && *desired.BatchSize != observed.BatchSize {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.batchSize", OldValue: observed.BatchSize, NewValue: *desired.BatchSize})
	}
	if desired.MaximumBatchingWindowInSeconds != nil && *desired.MaximumBatchingWindowInSeconds != observed.MaximumBatchingWindowInSeconds {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.maximumBatchingWindowInSeconds", OldValue: observed.MaximumBatchingWindowInSeconds, NewValue: *desired.MaximumBatchingWindowInSeconds})
	}
	if !filterCriteriaMatch(desired.FilterCriteria, observed.FilterCriteria) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.filterCriteria", OldValue: observed.FilterCriteria, NewValue: desired.FilterCriteria})
	}
	if desired.BisectBatchOnFunctionError != nil && *desired.BisectBatchOnFunctionError != observed.BisectBatchOnFunctionError {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.bisectBatchOnFunctionError", OldValue: observed.BisectBatchOnFunctionError, NewValue: *desired.BisectBatchOnFunctionError})
	}
	if !int32PtrMatch(desired.MaximumRetryAttempts, observed.MaximumRetryAttempts) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.maximumRetryAttempts", OldValue: observed.MaximumRetryAttempts, NewValue: desired.MaximumRetryAttempts})
	}
	if !int32PtrMatch(desired.MaximumRecordAgeInSeconds, observed.MaximumRecordAgeInSeconds) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.maximumRecordAgeInSeconds", OldValue: observed.MaximumRecordAgeInSeconds, NewValue: desired.MaximumRecordAgeInSeconds})
	}
	if desired.ParallelizationFactor != nil && *desired.ParallelizationFactor != observed.ParallelizationFactor {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.parallelizationFactor", OldValue: observed.ParallelizationFactor, NewValue: *desired.ParallelizationFactor})
	}
	if desired.TumblingWindowInSeconds != nil && *desired.TumblingWindowInSeconds != observed.TumblingWindowInSeconds {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.tumblingWindowInSeconds", OldValue: observed.TumblingWindowInSeconds, NewValue: *desired.TumblingWindowInSeconds})
	}
	if !destinationMatch(desired.DestinationConfig, observed.DestinationConfig) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.destinationConfig", OldValue: observed.DestinationConfig, NewValue: desired.DestinationConfig})
	}
	if !scalingMatch(desired.ScalingConfig, observed.ScalingConfig) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.scalingConfig", OldValue: observed.ScalingConfig, NewValue: desired.ScalingConfig})
	}
	if !slices.Equal(desired.FunctionResponseTypes, observed.FunctionResponseTypes) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.functionResponseTypes", OldValue: observed.FunctionResponseTypes, NewValue: desired.FunctionResponseTypes})
	}
	return diffs
}

func int32PtrMatch(a, b *int32) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func filterCriteriaMatch(a, b *FilterCriteriaSpec) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil || len(a.Filters) != len(b.Filters) {
		return false
	}
	for i := range a.Filters {
		if a.Filters[i].Pattern != b.Filters[i].Pattern {
			return false
		}
	}
	return true
}

func destinationMatch(a, b *DestinationSpec) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.OnFailure.DestinationArn == b.OnFailure.DestinationArn
}

func scalingMatch(a, b *ScalingSpec) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.MaximumConcurrency == b.MaximumConcurrency
}
