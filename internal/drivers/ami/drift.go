package ami

import (
	"sort"
	"strings"
	"time"
)

func HasDrift(desired AMISpec, observed ObservedState) bool {
	if observed.State != "available" {
		return false
	}

	if !tagsMatch(desired.Tags, observed.Tags) {
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

	desiredFiltered := filterPraxisTags(desired.Tags)
	observedFiltered := filterPraxisTags(observed.Tags)
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

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func tagsMatch(a, b map[string]string) bool {
	fa := filterPraxisTags(a)
	fb := filterPraxisTags(b)
	if len(fa) != len(fb) {
		return false
	}
	for key, value := range fa {
		if other, ok := fb[key]; !ok || other != value {
			return false
		}
	}
	return true
}

func filterPraxisTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(tags))
	for key, value := range tags {
		if !strings.HasPrefix(key, "praxis:") {
			out[key] = value
		}
	}
	return out
}

func hasLaunchPermDrift(desired *LaunchPermsSpec, observed ObservedState) bool {
	return !launchPermsMatch(desired, observed)
}

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

func normalizeLaunchPermSpec(spec *LaunchPermsSpec) LaunchPermsSpec {
	if spec == nil {
		return LaunchPermsSpec{}
	}
	accounts := append([]string(nil), spec.AccountIds...)
	sort.Strings(accounts)
	accounts = dedupe(accounts)
	return LaunchPermsSpec{AccountIds: accounts, Public: spec.Public}
}

func launchPermsFromObserved(observed ObservedState) *LaunchPermsSpec {
	if !observed.LaunchPermPublic && len(observed.LaunchPermAccounts) == 0 {
		return nil
	}
	return &LaunchPermsSpec{
		AccountIds: append([]string(nil), observed.LaunchPermAccounts...),
		Public:     observed.LaunchPermPublic,
	}
}

func hasDeprecationDrift(desired *DeprecationSpec, observed string) bool {
	return normalizeDeprecation(desired) != normalizeTimestamp(observed)
}

func normalizeDeprecation(desired *DeprecationSpec) string {
	if desired == nil {
		return ""
	}
	return normalizeTimestamp(desired.DeprecateAt)
}

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

func deprecationValue(spec *DeprecationSpec) any {
	if spec == nil {
		return nil
	}
	return spec.DeprecateAt
}

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
