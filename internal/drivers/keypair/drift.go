// Drift detection for EC2 Key Pairs.
// Key pairs have very limited mutability — only tags can be updated in-place.
// KeyType is immutable and reported as informational only.
package keypair

import (
	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift returns true if user tags differ between desired and observed state.
// Tags are the only mutable attribute on key pairs.
func HasDrift(desired KeyPairSpec, observed ObservedState) bool {
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
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
	desiredFiltered := drivers.FilterPraxisTags(desired)
	observedFiltered := drivers.FilterPraxisTags(observed)
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
