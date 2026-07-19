package eip

import (
	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift returns true if desired and observed state differ on mutable fields.
func HasDrift(desired ElasticIPSpec, observed ObservedState) bool {
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs returns field-level differences for plan output.
func ComputeFieldDiffs(desired ElasticIPSpec, observed ObservedState) []drivers.FieldDiff {
	return computeTagDiffs(desired.Tags, observed.Tags)
}

// computeTagDiffs returns per-tag diffs after filtering praxis: tags.
func computeTagDiffs(desired, observed map[string]string) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	desiredFiltered := drivers.FilterPraxisTags(desired)
	observedFiltered := drivers.FilterPraxisTags(observed)
	for key, value := range desiredFiltered {
		if observedValue, ok := observedFiltered[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if observedValue != value {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: observedValue, NewValue: value})
		}
	}
	for key, value := range observedFiltered {
		if _, ok := desiredFiltered[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}
