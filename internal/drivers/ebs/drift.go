package ebs

import (
	"fmt"
	"strings"
)

// HasDrift returns true if the desired spec and observed state differ on mutable fields.
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
	if !tagsMatch(desired.Tags, observed.Tags) {
		return true
	}

	return false
}

// ComputeFieldDiffs returns field-level differences for plan output.
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

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

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

func formatManagedKeyConflict(managedKey, volumeID string) error {
	return fmt.Errorf("volume name %q in this region is already managed by Praxis (volumeId: %s); remove the existing resource or use a different metadata.name", managedKey, volumeID)
}
