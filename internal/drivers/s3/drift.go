package s3

import "github.com/shirvan/praxis/internal/drivers"

// HasDrift compares the desired spec against the observed AWS state and returns
// true if they differ. This function handles AWS API quirks:
//
//   - AWS returns an empty string for versioning status on newly created buckets
//     that have never had versioning configured, but "Suspended" for buckets where
//     versioning was explicitly disabled. We treat both as "versioning is off."
//
//   - Tag comparison ignores order (maps are unordered by nature in Go).
//
//   - We do NOT compare ACL because AWS's GetBucketAcl returns a complex grant
//     structure that doesn't map cleanly to the simple ACL string. ACL drift
//     detection is a future improvement.
func HasDrift(desired S3BucketSpec, observed ObservedState) bool {
	// --- Check versioning ---
	desiredVersioning := "Suspended"
	if desired.Versioning {
		desiredVersioning = "Enabled"
	}
	// AWS returns empty string for buckets that have never had versioning configured.
	// Normalize to "Suspended" for comparison.
	observedVersioning := observed.VersioningStatus
	if observedVersioning == "" {
		observedVersioning = "Suspended"
	}
	if desiredVersioning != observedVersioning {
		return true
	}

	// --- Check encryption ---
	if desired.Encryption.Enabled {
		if observed.EncryptionAlgo != desired.Encryption.Algorithm {
			return true
		}
	}

	// --- Check tags ---
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		return true
	}

	return false
}

// ComputeFieldDiffs returns a list of specific field-level differences between
// the desired spec and observed state. This powers the `praxis plan` output —
// Praxis's equivalent of `praxis plan`.
//
// Each FieldDiff shows the path, old value, and new value for a changed field.
// Returns nil if there is no drift.
func ComputeFieldDiffs(desired S3BucketSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	// --- Versioning ---
	desiredVersioning := "Suspended"
	if desired.Versioning {
		desiredVersioning = "Enabled"
	}
	observedVersioning := observed.VersioningStatus
	if observedVersioning == "" {
		observedVersioning = "Suspended"
	}
	if desiredVersioning != observedVersioning {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.versioning",
			OldValue: observedVersioning,
			NewValue: desiredVersioning,
		})
	}

	// --- Encryption ---
	if desired.Encryption.Enabled && observed.EncryptionAlgo != desired.Encryption.Algorithm {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.encryption.algorithm",
			OldValue: observed.EncryptionAlgo,
			NewValue: desired.Encryption.Algorithm,
		})
	}

	// --- Tags: added or changed ---
	for k, v := range desired.Tags {
		if ov, ok := observed.Tags[k]; !ok {
			diffs = append(diffs, FieldDiffEntry{
				Path:     "tags." + k,
				OldValue: nil,
				NewValue: v,
			})
		} else if ov != v {
			diffs = append(diffs, FieldDiffEntry{
				Path:     "tags." + k,
				OldValue: ov,
				NewValue: v,
			})
		}
	}
	// Tags removed
	for k, v := range observed.Tags {
		if _, ok := desired.Tags[k]; !ok {
			diffs = append(diffs, FieldDiffEntry{
				Path:     "tags." + k,
				OldValue: v,
				NewValue: nil,
			})
		}
	}

	return diffs
}

// FieldDiffEntry represents a single field-level change between desired and observed.
// Used by the diff/plan engine to produce human-readable plan output.
type FieldDiffEntry struct {
	// Path is the dot-separated path to the field (e.g., "spec.versioning").
	Path string

	// OldValue is the current value (nil for new fields).
	OldValue any

	// NewValue is the desired value (nil for removed fields).
	NewValue any
}
