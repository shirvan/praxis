// Package dynamodbtable – drift.go
//
// This file implements drift detection for DynamoDB tables. HasDrift compares the
// desired spec against the observed state from AWS and returns true when any
// mutable field has diverged. ComputeFieldDiffs produces a structured list of
// individual field changes for plan output and logging; immutable fields (the
// primary key schema) are annotated with "(immutable, requires replacement)".
package dynamodbtable

import (
	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift compares the desired DynamoDBTable spec against the observed state
// from AWS and returns true if any mutable field has diverged. It is called
// during Reconcile to decide whether drift correction is needed. Immutable
// fields (the primary key schema) are intentionally excluded — they cannot be
// corrected in place.
func HasDrift(desired DynamoDBTableSpec, observed ObservedState) bool {
	if configDrift(desired, observed) {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// configDrift reports whether any field converged via UpdateTable has diverged
// from the observed state.
func configDrift(desired DynamoDBTableSpec, observed ObservedState) bool {
	if billingModeOrDefault(desired.BillingMode) != billingModeOrDefault(observed.BillingMode) {
		return true
	}
	if isProvisioned(desired.BillingMode) {
		if capacityOrDefault(desired.ReadCapacity) != observed.ReadCapacity {
			return true
		}
		if capacityOrDefault(desired.WriteCapacity) != observed.WriteCapacity {
			return true
		}
	}
	return false
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging.
func ComputeFieldDiffs(desired DynamoDBTableSpec, observed ObservedState) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff

	// Immutable fields — reported for visibility, never corrected in place.
	if observed.HashKey != "" && desired.HashKey != observed.HashKey {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.hashKey (immutable, requires replacement)", OldValue: observed.HashKey, NewValue: desired.HashKey})
	}
	if observed.HashKey != "" && keyTypeOrDefault(desired.HashKeyType) != observed.HashKeyType {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.hashKeyType (immutable, requires replacement)", OldValue: observed.HashKeyType, NewValue: keyTypeOrDefault(desired.HashKeyType)})
	}
	if desired.RangeKey != observed.RangeKey {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.rangeKey (immutable, requires replacement)", OldValue: observed.RangeKey, NewValue: desired.RangeKey})
	}
	if desired.RangeKey != "" && observed.RangeKey != "" && keyTypeOrDefault(desired.RangeKeyType) != observed.RangeKeyType {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.rangeKeyType (immutable, requires replacement)", OldValue: observed.RangeKeyType, NewValue: keyTypeOrDefault(desired.RangeKeyType)})
	}

	// Mutable fields.
	if billingModeOrDefault(desired.BillingMode) != billingModeOrDefault(observed.BillingMode) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.billingMode", OldValue: observed.BillingMode, NewValue: billingModeOrDefault(desired.BillingMode)})
	}
	if isProvisioned(desired.BillingMode) {
		if capacityOrDefault(desired.ReadCapacity) != observed.ReadCapacity {
			diffs = append(diffs, drivers.FieldDiff{Path: "spec.readCapacity", OldValue: observed.ReadCapacity, NewValue: capacityOrDefault(desired.ReadCapacity)})
		}
		if capacityOrDefault(desired.WriteCapacity) != observed.WriteCapacity {
			diffs = append(diffs, drivers.FieldDiff{Path: "spec.writeCapacity", OldValue: observed.WriteCapacity, NewValue: capacityOrDefault(desired.WriteCapacity)})
		}
	}

	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

func computeTagDiffs(desired, observed map[string]string) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	cleanDesired := drivers.FilterPraxisTags(desired)
	cleanObserved := drivers.FilterPraxisTags(observed)
	for key, value := range cleanDesired {
		if current, ok := cleanObserved[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range cleanObserved {
		if _, ok := cleanDesired[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}
