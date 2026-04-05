// drift.go contains drift detection logic for Amazon Machine Images.
//
// Drift is only evaluated when the AMI is in "available" state.
// Mutable fields checked: description, tags (excluding praxis: prefix),
// launch permissions (account list + public flag), and deprecation schedule.
// Immutable fields (name, architecture, virtualizationType, rootDeviceName)
// are reported as informational diffs but never corrected.

package ami

import (
	"github.com/shirvan/praxis/internal/drivers"
	"sort"
	"strings"
	"time"
)

// HasDrift returns true when any mutable AMI attribute differs between desired and observed.
func HasDrift(desired AMISpec, observed ObservedState) bool {
	if observed.State != "available" {
		return false
	}

	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		return true
	}
	if desired.Description != observed.Description {
		return true
	}
	if hasLaunchPermDrift(desired.LaunchPermissions, observed) {
		return true
	}
	if hasDeprecationDrift(desired.Deprecation, observed.DeprecationTime) {
		return true
	}
	return false
}

// ComputeFieldDiffs returns field-level differences for plan output and observability.
// Includes both actionable mutable diffs and informational immutable diffs.
func ComputeFieldDiffs(desired AMISpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.Description != observed.Description {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.description",
			OldValue: observed.Description,
			NewValue: desired.Description,
		})
	}

	if !launchPermsMatch(desired.LaunchPermissions, observed) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.launchPermissions",
			OldValue: launchPermsFromObserved(observed),
			NewValue: normalizeLaunchPermSpec(desired.LaunchPermissions),
		})
	}

	if hasDeprecationDrift(desired.Deprecation, observed.DeprecationTime) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.deprecation.deprecateAt",
			OldValue: observed.DeprecationTime,
			NewValue: deprecationValue(desired.Deprecation),
		})
	}

	desiredFiltered := drivers.FilterPraxisTags(desired.Tags)
	observedFiltered := drivers.FilterPraxisTags(observed.Tags)
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

	if desired.Name != observed.Name && observed.Name != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.name (immutable, ignored)",
			OldValue: observed.Name,
			NewValue: desired.Name,
		})
	}
	if desired.Source.FromSnapshot != nil && desired.Source.FromSnapshot.Architecture != observed.Architecture && observed.Architecture != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.source.architecture (immutable, ignored)",
			OldValue: observed.Architecture,
			NewValue: desired.Source.FromSnapshot.Architecture,
		})
	}
	if desired.Source.FromSnapshot != nil && desired.Source.FromSnapshot.VirtualizationType != observed.VirtualizationType && observed.VirtualizationType != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.source.virtualizationType (immutable, ignored)",
			OldValue: observed.VirtualizationType,
			NewValue: desired.Source.FromSnapshot.VirtualizationType,
		})
	}
	if desired.Source.FromSnapshot != nil && desired.Source.FromSnapshot.RootDeviceName != observed.RootDeviceName && observed.RootDeviceName != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.source.rootDeviceName (immutable, ignored)",
			OldValue: observed.RootDeviceName,
			NewValue: desired.Source.FromSnapshot.RootDeviceName,
		})
	}

	return diffs
}

// FieldDiffEntry represents a single field-level difference between desired and observed.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// hasLaunchPermDrift returns true if launch permissions differ between desired and observed.
func hasLaunchPermDrift(desired *LaunchPermsSpec, observed ObservedState) bool {
	return !launchPermsMatch(desired, observed)
}

// launchPermsMatch compares normalized launch permission specs.
func launchPermsMatch(desired *LaunchPermsSpec, observed ObservedState) bool {
	normalizedDesired := normalizeLaunchPermSpec(desired)
	normalizedObserved := normalizeLaunchPermSpec(launchPermsFromObserved(observed))
	if normalizedDesired.Public != normalizedObserved.Public {
		return false
	}
	if len(normalizedDesired.AccountIds) != len(normalizedObserved.AccountIds) {
		return false
	}
	for index := range normalizedDesired.AccountIds {
		if normalizedDesired.AccountIds[index] != normalizedObserved.AccountIds[index] {
			return false
		}
	}
	return true
}

// normalizeLaunchPermSpec normalizes a LaunchPermsSpec for deterministic comparison:
// sorts account IDs and deduplicates them. Treats nil as empty.
func normalizeLaunchPermSpec(spec *LaunchPermsSpec) LaunchPermsSpec {
	if spec == nil {
		return LaunchPermsSpec{}
	}
	accounts := append([]string(nil), spec.AccountIds...)
	sort.Strings(accounts)
	accounts = dedupe(accounts)
	return LaunchPermsSpec{AccountIds: accounts, Public: spec.Public}
}

// launchPermsFromObserved reconstructs a LaunchPermsSpec from observed state.
func launchPermsFromObserved(observed ObservedState) *LaunchPermsSpec {
	if !observed.LaunchPermPublic && len(observed.LaunchPermAccounts) == 0 {
		return nil
	}
	return &LaunchPermsSpec{
		AccountIds: append([]string(nil), observed.LaunchPermAccounts...),
		Public:     observed.LaunchPermPublic,
	}
}

// hasDeprecationDrift returns true if the deprecation schedule differs.
// Both values are normalized to UTC RFC3339 before comparison.
func hasDeprecationDrift(desired *DeprecationSpec, observed string) bool {
	return normalizeDeprecation(desired) != normalizeTimestamp(observed)
}

// normalizeDeprecation extracts and normalizes the deprecateAt value from the spec.
func normalizeDeprecation(desired *DeprecationSpec) string {
	if desired == nil {
		return ""
	}
	return normalizeTimestamp(desired.DeprecateAt)
}

// normalizeTimestamp parses an RFC3339 timestamp and re-formats it in UTC for comparison.
func normalizeTimestamp(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return parsed.UTC().Format(time.RFC3339)
}

// deprecationValue returns the deprecateAt string or nil if no deprecation is set.
func deprecationValue(spec *DeprecationSpec) any {
	if spec == nil {
		return nil
	}
	return spec.DeprecateAt
}

// dedupe removes consecutive duplicate strings from a sorted slice in-place.
func dedupe(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := values[:0]
	var prev string
	for index, value := range values {
		if index == 0 || value != prev {
			out = append(out, value)
			prev = value
		}
	}
	return out
}
