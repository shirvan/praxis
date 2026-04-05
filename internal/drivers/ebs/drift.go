package ebs

import (
	"fmt"
	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift returns true if the desired spec and observed state differ on mutable fields.
// Only checks drift when the volume is in "available" or "in-use" state — volumes
// in transient states ("creating", "deleting") are not compared.
//
// Compared fields: volumeType, sizeGiB, iops (if specified), throughput (if specified), tags.
// Immutable fields (availabilityZone, encrypted, kmsKeyId) are NOT compared here
// because they cannot be changed after creation.
func HasDrift(desired EBSVolumeSpec, observed ObservedState) bool {
	if observed.State != "available" && observed.State != "in-use" {
		return false
	}

	if desired.VolumeType != observed.VolumeType {
		return true
	}
	if desired.SizeGiB != observed.SizeGiB {
		return true
	}
	if desired.Iops > 0 && desired.Iops != observed.Iops {
		return true
	}
	if desired.Throughput > 0 && desired.Throughput != observed.Throughput {
		return true
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		return true
	}

	return false
}

// ComputeFieldDiffs returns field-level differences for plan output.
// This powers the `praxis plan` output showing what would change.
// Immutable fields are annotated with "(immutable, ignored)" in the path
// to inform operators that those changes cannot be applied.
// Size shrink is annotated with "(shrink not supported, ignored)".
func ComputeFieldDiffs(desired EBSVolumeSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.VolumeType != observed.VolumeType {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.volumeType",
			OldValue: observed.VolumeType,
			NewValue: desired.VolumeType,
		})
	}

	if desired.SizeGiB != observed.SizeGiB {
		path := "spec.sizeGiB"
		if desired.SizeGiB < observed.SizeGiB {
			path = "spec.sizeGiB (shrink not supported, ignored)"
		}
		diffs = append(diffs, FieldDiffEntry{
			Path:     path,
			OldValue: observed.SizeGiB,
			NewValue: desired.SizeGiB,
		})
	}

	if desired.Iops > 0 && desired.Iops != observed.Iops {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.iops",
			OldValue: observed.Iops,
			NewValue: desired.Iops,
		})
	}

	if desired.Throughput > 0 && desired.Throughput != observed.Throughput {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.throughput",
			OldValue: observed.Throughput,
			NewValue: desired.Throughput,
		})
	}

	if desired.AvailabilityZone != observed.AvailabilityZone && observed.AvailabilityZone != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.availabilityZone (immutable, ignored)",
			OldValue: observed.AvailabilityZone,
			NewValue: desired.AvailabilityZone,
		})
	}

	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)

	return diffs
}

// FieldDiffEntry represents a single field-level change between desired and observed.
// Used by the diff/plan engine to produce human-readable plan output.
type FieldDiffEntry struct {
	// Path is the dot-separated path to the field (e.g., "spec.volumeType").
	Path string

	// OldValue is the current value in AWS (nil for new fields).
	OldValue any

	// NewValue is the desired value (nil for removed fields).
	NewValue any
}

// computeTagDiffs produces per-tag diff entries for added, changed, and removed tags.
// Tags prefixed with "praxis:" are filtered out (they are internal ownership markers).
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

// formatManagedKeyConflict produces a human-readable error when a volume with
// the same managed key already exists. This indicates a naming collision.
func formatManagedKeyConflict(managedKey, volumeID string) error {
	return fmt.Errorf("volume name %q in this region is already managed by Praxis (volumeId: %s); remove the existing resource or use a different metadata.name", managedKey, volumeID)
}
