// Package acmcert – drift.go
//
// This file implements drift detection for AWS ACM Certificate.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package acmcert

import (
	"github.com/shirvan/praxis/internal/drivers"
)

// FieldDiffEntry represents a single field-level difference between the desired
// spec and the observed state. Path uses dot notation (e.g. "spec.name");
// immutable fields are annotated with "(immutable, requires replacement)".
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift compares the desired ACMCertificate spec against the observed
// state from AWS and returns true if any mutable field has diverged.
// It is called during Reconcile to decide whether drift correction is needed.
func HasDrift(desired ACMCertificateSpec, observed ObservedState) bool {
	if normalizeTransparencyPreference(desired.Options) != normalizeTransparencyPreference(&observed.Options) {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging. Immutable field changes are clearly annotated.
func ComputeFieldDiffs(desired ACMCertificateSpec, observed ObservedState) []FieldDiffEntry {
	diffs := make([]FieldDiffEntry, 0, 4)
	if normalizeTransparencyPreference(desired.Options) != normalizeTransparencyPreference(&observed.Options) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "options.certificateTransparencyLoggingPreference",
			OldValue: normalizeTransparencyPreference(&observed.Options),
			NewValue: normalizeTransparencyPreference(desired.Options),
		})
	}
	return append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
}

func computeTagDiffs(desired, observed map[string]string) []FieldDiffEntry {
	desiredFiltered := drivers.FilterPraxisTags(desired)
	observedFiltered := drivers.FilterPraxisTags(observed)
	diffs := make([]FieldDiffEntry, 0, len(desiredFiltered)+len(observedFiltered))
	for key, value := range desiredFiltered {
		if observedValue, ok := observedFiltered[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if observedValue != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: observedValue, NewValue: value})
		}
	}
	for key, value := range observedFiltered {
		if _, ok := desiredFiltered[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}
