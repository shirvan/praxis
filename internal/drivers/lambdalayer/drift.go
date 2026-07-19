// Drift detection for Lambda Layers.
// Since layer versions are immutable, "drift" means either
// (a) an external publish created a newer version, or (b) permission changes.
// Configuration fields (description, runtimes, architectures, license) are
// reported as diffs but cannot be corrected in-place — they require a new publish.
package lambdalayer

import (
	"github.com/shirvan/praxis/internal/drivers"
	"slices"
	"sort"
)

// HasDrift returns true if any field differs between desired and observed state.
func HasDrift(desired LambdaLayerSpec, observed ObservedState, outputs LambdaLayerOutputs) bool {
	return len(ComputeFieldDiffs(desired, observed, outputs)) > 0
}

// ComputeFieldDiffs returns per-field diffs.
// Checks: version mismatch (external publish), description, compatibleRuntimes,
// compatibleArchitectures, licenseInfo, and permissions (accountIds + public flag).
func ComputeFieldDiffs(desired LambdaLayerSpec, observed ObservedState, outputs LambdaLayerOutputs) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	if outputs.Version != 0 && observed.Version != outputs.Version {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.version (external publish detected)", OldValue: observed.Version, NewValue: outputs.Version})
	}
	if desired.Description != observed.Description {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.description", OldValue: observed.Description, NewValue: desired.Description})
	}
	if !sortedSlicesEqual(desired.CompatibleRuntimes, observed.CompatibleRuntimes) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.compatibleRuntimes", OldValue: observed.CompatibleRuntimes, NewValue: desired.CompatibleRuntimes})
	}
	if !sortedSlicesEqual(desired.CompatibleArchitectures, observed.CompatibleArchitectures) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.compatibleArchitectures", OldValue: observed.CompatibleArchitectures, NewValue: desired.CompatibleArchitectures})
	}
	if desired.LicenseInfo != observed.LicenseInfo {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.licenseInfo", OldValue: observed.LicenseInfo, NewValue: desired.LicenseInfo})
	}
	desiredPerms := PermissionsSpec{}
	if desired.Permissions != nil {
		desiredPerms = normalizePermissions(*desired.Permissions)
	}
	observedPerms := normalizePermissions(observed.Permissions)
	if !slices.Equal(desiredPerms.AccountIds, observedPerms.AccountIds) || desiredPerms.Public != observedPerms.Public {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.permissions", OldValue: observedPerms, NewValue: desiredPerms})
	}
	return diffs
}

// sortedSlicesEqual compares two string slices for equality after sorting copies,
// so that order differences (e.g. [arm64, x86_64] vs [x86_64, arm64]) do not produce false diffs.
func sortedSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	return slices.Equal(ac, bc)
}
