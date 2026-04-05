// Package metricalarm – drift.go
//
// This file implements drift detection for AWS CloudWatch Metric Alarm.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package metricalarm

import (
	"cmp"
	"github.com/shirvan/praxis/internal/drivers"
	"slices"
)

// HasDrift compares the desired MetricAlarm spec against the observed
// state from AWS and returns true if any mutable field has diverged.
// It is called during Reconcile to decide whether drift correction is needed.
func HasDrift(desired MetricAlarmSpec, observed ObservedState) bool {
	if desired.Namespace != observed.Namespace {
		return true
	}
	if desired.MetricName != observed.MetricName {
		return true
	}
	if !dimensionsMatch(desired.Dimensions, observed.Dimensions) {
		return true
	}
	if desired.Statistic != observed.Statistic {
		return true
	}
	if desired.ExtendedStatistic != observed.ExtendedStatistic {
		return true
	}
	if desired.Period != observed.Period {
		return true
	}
	if desired.EvaluationPeriods != observed.EvaluationPeriods {
		return true
	}
	if !datapointsMatch(desired.DatapointsToAlarm, observed.DatapointsToAlarm, desired.EvaluationPeriods) {
		return true
	}
	if desired.Threshold != observed.Threshold {
		return true
	}
	if desired.ComparisonOperator != observed.ComparisonOperator {
		return true
	}
	if desired.TreatMissingData != observed.TreatMissingData {
		return true
	}
	if desired.AlarmDescription != observed.AlarmDescription {
		return true
	}
	if desired.ActionsEnabled != observed.ActionsEnabled {
		return true
	}
	if !sliceEqual(desired.AlarmActions, observed.AlarmActions) {
		return true
	}
	if !sliceEqual(desired.OKActions, observed.OKActions) {
		return true
	}
	if !sliceEqual(desired.InsufficientDataActions, observed.InsufficientDataActions) {
		return true
	}
	if desired.Unit != observed.Unit {
		return true
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		return true
	}
	return false
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging. Immutable field changes are clearly annotated.
func ComputeFieldDiffs(desired MetricAlarmSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	appendIfDiff := func(path string, oldValue, newValue any, changed bool) {
		if changed {
			diffs = append(diffs, FieldDiffEntry{Path: path, OldValue: oldValue, NewValue: newValue})
		}
	}
	appendIfDiff("spec.namespace", observed.Namespace, desired.Namespace, desired.Namespace != observed.Namespace)
	appendIfDiff("spec.metricName", observed.MetricName, desired.MetricName, desired.MetricName != observed.MetricName)
	appendIfDiff("spec.dimensions", observed.Dimensions, desired.Dimensions, !dimensionsMatch(desired.Dimensions, observed.Dimensions))
	appendIfDiff("spec.statistic", observed.Statistic, desired.Statistic, desired.Statistic != observed.Statistic)
	appendIfDiff("spec.extendedStatistic", observed.ExtendedStatistic, desired.ExtendedStatistic, desired.ExtendedStatistic != observed.ExtendedStatistic)
	appendIfDiff("spec.period", observed.Period, desired.Period, desired.Period != observed.Period)
	appendIfDiff("spec.evaluationPeriods", observed.EvaluationPeriods, desired.EvaluationPeriods, desired.EvaluationPeriods != observed.EvaluationPeriods)
	appendIfDiff("spec.datapointsToAlarm", observed.DatapointsToAlarm, effectiveDatapoints(desired.DatapointsToAlarm, desired.EvaluationPeriods), !datapointsMatch(desired.DatapointsToAlarm, observed.DatapointsToAlarm, desired.EvaluationPeriods))
	appendIfDiff("spec.threshold", observed.Threshold, desired.Threshold, desired.Threshold != observed.Threshold)
	appendIfDiff("spec.comparisonOperator", observed.ComparisonOperator, desired.ComparisonOperator, desired.ComparisonOperator != observed.ComparisonOperator)
	appendIfDiff("spec.treatMissingData", observed.TreatMissingData, desired.TreatMissingData, desired.TreatMissingData != observed.TreatMissingData)
	appendIfDiff("spec.alarmDescription", observed.AlarmDescription, desired.AlarmDescription, desired.AlarmDescription != observed.AlarmDescription)
	appendIfDiff("spec.actionsEnabled", observed.ActionsEnabled, desired.ActionsEnabled, desired.ActionsEnabled != observed.ActionsEnabled)
	appendIfDiff("spec.alarmActions", observed.AlarmActions, desired.AlarmActions, !sliceEqual(desired.AlarmActions, observed.AlarmActions))
	appendIfDiff("spec.okActions", observed.OKActions, desired.OKActions, !sliceEqual(desired.OKActions, observed.OKActions))
	appendIfDiff("spec.insufficientDataActions", observed.InsufficientDataActions, desired.InsufficientDataActions, !sliceEqual(desired.InsufficientDataActions, observed.InsufficientDataActions))
	appendIfDiff("spec.unit", observed.Unit, desired.Unit, desired.Unit != observed.Unit)
	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

// FieldDiffEntry represents a single field-level difference between the desired
// spec and the observed state. Path uses dot notation (e.g. "spec.name");
// immutable fields are annotated with "(immutable, requires replacement)".
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func dimensionsMatch(a, b map[string]string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || other != value {
			return false
		}
	}
	return true
}

func datapointsMatch(desired *int32, observed int32, evaluationPeriods int32) bool {
	return effectiveDatapoints(desired, evaluationPeriods) == observed
}

func effectiveDatapoints(desired *int32, evaluationPeriods int32) int32 {
	if desired == nil {
		return evaluationPeriods
	}
	return *desired
}

func sliceEqual(a, b []string) bool {
	aCopy := append([]string(nil), a...)
	bCopy := append([]string(nil), b...)
	slices.Sort(aCopy)
	slices.Sort(bCopy)
	return slices.CompareFunc(aCopy, bCopy, cmp.Compare) == 0
}

func computeTagDiffs(desired, observed map[string]string) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	cleanDesired := drivers.FilterPraxisTags(desired)
	cleanObserved := drivers.FilterPraxisTags(observed)
	for key, value := range cleanDesired {
		if current, ok := cleanObserved[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range cleanObserved {
		if _, ok := cleanDesired[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}
