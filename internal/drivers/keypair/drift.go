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
	return desired.KeyName != observed.KeyName || desired.KeyType != observed.KeyType ||
		!drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs returns a list of individual field differences.
// KeyType is reported as an immutable informational diff; tag diffs are actionable.
func ComputeFieldDiffs(desired KeyPairSpec, observed ObservedState) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff

	if desired.KeyType != observed.KeyType && observed.KeyType != "" {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.keyType (immutable, ignored)",
			OldValue: observed.KeyType,
			NewValue: desired.KeyType,
		})
	}

	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

// computeTagDiffs produces per-key diffs for added, changed, and removed tags.
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
