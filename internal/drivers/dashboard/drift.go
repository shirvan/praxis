package dashboard

import (
	"encoding/json"
	"reflect"
)

func HasDrift(desired DashboardSpec, observed ObservedState) bool {
	return !bodiesEqual(desired.DashboardBody, observed.DashboardBody)
}

func ComputeFieldDiffs(desired DashboardSpec, observed ObservedState) []FieldDiffEntry {
	if bodiesEqual(desired.DashboardBody, observed.DashboardBody) {
		return nil
	}
	return []FieldDiffEntry{{
		Path:     "spec.dashboardBody",
		OldValue: truncateBody(observed.DashboardBody, 200),
		NewValue: truncateBody(desired.DashboardBody, 200),
	}}
}

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
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
