// Package dashboard – drift.go
//
// This file implements drift detection for AWS CloudWatch Dashboard.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package dashboard

import (
	"encoding/json"
	"github.com/shirvan/praxis/internal/drivers"
	"reflect"
)

// HasDrift compares the desired Dashboard spec against the observed
// state from AWS and returns true if any mutable field has diverged.
// It is called during Reconcile to decide whether drift correction is needed.
func HasDrift(desired DashboardSpec, observed ObservedState) bool {
	return !bodiesEqual(desired.DashboardBody, observed.DashboardBody)
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging. Immutable field changes are clearly annotated.
func ComputeFieldDiffs(desired DashboardSpec, observed ObservedState) []drivers.FieldDiff {
	if bodiesEqual(desired.DashboardBody, observed.DashboardBody) {
		return nil
	}
	return []drivers.FieldDiff{{
		Path:     "spec.dashboardBody",
		OldValue: truncateBody(observed.DashboardBody, 200),
		NewValue: truncateBody(desired.DashboardBody, 200),
	}}
}

func bodiesEqual(desired, observed string) bool {
	var desiredAny any
	var observedAny any
	if err := json.Unmarshal([]byte(desired), &desiredAny); err != nil {
		return desired == observed
	}
	if err := json.Unmarshal([]byte(observed), &observedAny); err != nil {
		return false
	}
	return reflect.DeepEqual(desiredAny, observedAny)
}

func truncateBody(body string, n int) string {
	if len(body) <= n {
		return body
	}
	return body[:n] + "..."
}
