package ebs

import "github.com/shirvan/praxis/internal/drivers"

// HasDrift returns true if the desired spec and observed state differ on mutable fields.
// Only checks drift when the volume is in "available" or "in-use" state — volumes
// in transient states ("creating", "deleting") are not compared.
//
// Compared fields: volumeType, sizeGiB, iops (if specified), throughput (if specified), tags.
// Immutable differences are surfaced so Converge can reject them with a
// replacement-required conflict rather than persisting an impossible desired state.
func HasDrift(desired EBSVolumeSpec, observed ObservedState) bool {
	if observed.State != "available" && observed.State != "in-use" {
		return false
	}
	if desired.AvailabilityZone != observed.AvailabilityZone || desired.Encrypted != observed.Encrypted ||
		(desired.SnapshotId != "" && desired.SnapshotId != observed.SnapshotId) {
		return true
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
// Immutable fields and size shrink are annotated as requiring replacement.
func ComputeFieldDiffs(desired EBSVolumeSpec, observed ObservedState) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff

	if desired.VolumeType != observed.VolumeType {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.volumeType",
			OldValue: observed.VolumeType,
			NewValue: desired.VolumeType,
		})
	}

	if desired.SizeGiB != observed.SizeGiB {
		path := "spec.sizeGiB"
		if desired.SizeGiB < observed.SizeGiB {
			path = "spec.sizeGiB (shrink not supported, requires replacement)"
		}
		diffs = append(diffs, drivers.FieldDiff{
			Path:     path,
			OldValue: observed.SizeGiB,
			NewValue: desired.SizeGiB,
		})
	}

	if desired.Iops > 0 && desired.Iops != observed.Iops {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.iops",
			OldValue: observed.Iops,
			NewValue: desired.Iops,
		})
	}

	if desired.Throughput > 0 && desired.Throughput != observed.Throughput {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.throughput",
			OldValue: observed.Throughput,
			NewValue: desired.Throughput,
		})
	}

	if desired.AvailabilityZone != observed.AvailabilityZone && observed.AvailabilityZone != "" {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.availabilityZone (immutable, requires replacement)",
			OldValue: observed.AvailabilityZone,
			NewValue: desired.AvailabilityZone,
		})
	}

	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)

	return diffs
}

// computeTagDiffs produces per-tag diff entries for added, changed, and removed tags.
// Tags prefixed with "praxis:" are filtered out (they are internal ownership markers).
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
