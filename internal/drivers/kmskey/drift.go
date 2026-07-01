// Package kmskey – drift.go
//
// This file implements drift detection for KMS keys. HasDrift compares the
// desired spec against the observed state from AWS and returns true when any
// mutable field (description, rotation, tags) has diverged. ComputeFieldDiffs
// produces a structured list of individual field changes for plan output and
// logging; immutable fields (key usage, key spec) are annotated with
// "(immutable, requires replacement)" and never reported as correctable drift.
package kmskey

import (
	"sort"

	"github.com/shirvan/praxis/internal/drivers"
)

// FieldDiffEntry represents a single field-level difference between the desired
// spec and the observed state. Path uses dot notation (e.g. "spec.description").
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift compares the desired KMSKey spec against the observed state from AWS
// and returns true if any mutable field has diverged. It is called during
// Reconcile to decide whether drift correction is needed. Immutable fields (key
// usage, key spec) are intentionally excluded — they cannot be corrected in place.
func HasDrift(desired KMSKeySpec, observed ObservedState) bool {
	if configDrift(desired, observed) {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// configDrift reports whether any field converged in place (description,
// rotation) has diverged from the observed state.
func configDrift(desired KMSKeySpec, observed ObservedState) bool {
	if desired.Description != observed.Description {
		return true
	}
	return desired.EnableKeyRotation != observed.EnableKeyRotation
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging.
func ComputeFieldDiffs(desired KMSKeySpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	// Immutable fields — reported for visibility, never corrected in place.
	if observed.KeyUsage != "" && desired.KeyUsage != observed.KeyUsage {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.keyUsage (immutable, requires replacement)", OldValue: observed.KeyUsage, NewValue: desired.KeyUsage})
	}
	if observed.KeySpec != "" && desired.KeySpec != observed.KeySpec {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.keySpec (immutable, requires replacement)", OldValue: observed.KeySpec, NewValue: desired.KeySpec})
	}

	// Mutable fields.
	if desired.Description != observed.Description {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.description", OldValue: observed.Description, NewValue: desired.Description})
	}
	if desired.EnableKeyRotation != observed.EnableKeyRotation {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.enableKeyRotation", OldValue: observed.EnableKeyRotation, NewValue: desired.EnableKeyRotation})
	}

	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
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

// sortedKeys returns the map's keys in ascending order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// sortStrings sorts a slice of strings in place, ascending.
func sortStrings(in []string) {
	sort.Strings(in)
}
