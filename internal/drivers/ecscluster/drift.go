// Package ecscluster – drift.go
//
// This file implements drift detection for ECS clusters. HasDrift compares the
// desired spec against the observed state from AWS and returns true when any
// mutable field has diverged. ComputeFieldDiffs produces a structured list of
// individual field changes for plan output and logging. An ECS cluster has no
// immutable spec fields beyond its identity, so every diff is correctable.
package ecscluster

import (
	"sort"

	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift compares the desired ECSCluster spec against the observed state from
// AWS and returns true if any mutable field has diverged. It is called during
// Reconcile to decide whether drift correction is needed.
func HasDrift(desired ECSClusterSpec, observed ObservedState) bool {
	if desired.Name != observed.Name {
		return true
	}
	if containerInsightsDrift(desired, observed) {
		return true
	}
	if capacityProvidersDrift(desired, observed) {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// containerInsightsDrift reports whether the desired Container Insights setting
// differs from the observed one. An empty desired value normalizes to the AWS
// default (disabled).
func containerInsightsDrift(desired ECSClusterSpec, observed ObservedState) bool {
	return normalizeContainerInsights(desired.ContainerInsights) != normalizeContainerInsights(observed.ContainerInsights)
}

// capacityProvidersDrift reports whether the desired set of capacity providers
// differs from the observed set, ignoring order.
func capacityProvidersDrift(desired ECSClusterSpec, observed ObservedState) bool {
	return !stringSetEqual(desired.CapacityProviders, observed.CapacityProviders)
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging.
func ComputeFieldDiffs(desired ECSClusterSpec, observed ObservedState) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	if desired.Name != observed.Name {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.name (immutable, requires replacement)", OldValue: observed.Name, NewValue: desired.Name})
	}

	if containerInsightsDrift(desired, observed) {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.containerInsights",
			OldValue: normalizeContainerInsights(observed.ContainerInsights),
			NewValue: normalizeContainerInsights(desired.ContainerInsights),
		})
	}
	if capacityProvidersDrift(desired, observed) {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.capacityProviders",
			OldValue: sortedCopy(observed.CapacityProviders),
			NewValue: sortedCopy(desired.CapacityProviders),
		})
	}

	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

// normalizeContainerInsights treats an unset value as the AWS default of
// "disabled" so an omitted spec field never registers as drift.
func normalizeContainerInsights(value string) string {
	if value == "" {
		return defaultContainerInsights
	}
	return value
}

// stringSetEqual reports whether two slices contain the same set of values,
// ignoring order and duplicates.
func stringSetEqual(a, b []string) bool {
	sa, sb := map[string]struct{}{}, map[string]struct{}{}
	for _, v := range a {
		sa[v] = struct{}{}
	}
	for _, v := range b {
		sb[v] = struct{}{}
	}
	if len(sa) != len(sb) {
		return false
	}
	for v := range sa {
		if _, ok := sb[v]; !ok {
			return false
		}
	}
	return true
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

func computeTagDiffs(desired, observed map[string]string) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	cleanDesired := drivers.FilterPraxisTags(desired)
	cleanObserved := drivers.FilterPraxisTags(observed)
	for key, value := range cleanDesired {
		if current, ok := cleanObserved[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range cleanObserved {
		if _, ok := cleanDesired[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}
