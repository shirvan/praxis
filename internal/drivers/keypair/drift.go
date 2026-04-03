// Drift detection for EC2 Key Pairs.
// Key pairs have very limited mutability — only tags can be updated in-place.
// KeyType is immutable and reported as informational only.
package keypair

import "strings"

// HasDrift returns true if user tags differ between desired and observed state.
// Tags are the only mutable attribute on key pairs.
func HasDrift(desired KeyPairSpec, observed ObservedState) bool {
	return !tagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs returns a list of individual field differences.
// KeyType is reported as an immutable informational diff; tag diffs are actionable.
func ComputeFieldDiffs(desired KeyPairSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.KeyType != observed.KeyType && observed.KeyType != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.keyType (immutable, ignored)",
			OldValue: observed.KeyType,
			NewValue: desired.KeyType,
		})
	}

	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

// FieldDiffEntry represents a single field difference with JSON path and old/new values.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// computeTagDiffs produces per-key diffs for added, changed, and removed tags.
func computeTagDiffs(desired, observed map[string]string) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	desiredFiltered := filterPraxisTags(desired)
	observedFiltered := filterPraxisTags(observed)
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

// tagsMatch compares two tag maps excluding praxis: namespace tags.
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

// filterPraxisTags returns a copy of the map with all praxis: prefixed keys excluded.
func filterPraxisTags(m map[string]string) map[string]string {
	if len(m) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for key, value := range m {
		if !strings.HasPrefix(key, "praxis:") {
			out[key] = value
		}
	}
	return out
}
