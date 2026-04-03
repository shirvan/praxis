// Drift detection for Lambda Layers.
// Since layer versions are immutable, "drift" means either
// (a) an external publish created a newer version, or (b) permission changes.
// Configuration fields (description, runtimes, architectures, license) are
// reported as diffs but cannot be corrected in-place — they require a new publish.
package lambdalayer

import "slices"

// FieldDiffEntry represents a single field difference with JSON path and old/new values.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift returns true if any field differs between desired and observed state.
func HasDrift(desired LambdaLayerSpec, observed ObservedState, outputs LambdaLayerOutputs) bool {
	return len(ComputeFieldDiffs(desired, observed, outputs)) > 0
}

// ComputeFieldDiffs returns per-field diffs.
// Checks: version mismatch (external publish), description, compatibleRuntimes,
// compatibleArchitectures, licenseInfo, and permissions (accountIds + public flag).
func ComputeFieldDiffs(desired LambdaLayerSpec, observed ObservedState, outputs LambdaLayerOutputs) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	if outputs.Version != 0 && observed.Version != outputs.Version {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.version (external publish detected)", OldValue: observed.Version, NewValue: outputs.Version})
	}
	if desired.Description != observed.Description {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.description", OldValue: observed.Description, NewValue: desired.Description})
	}
	if !slices.Equal(desired.CompatibleRuntimes, observed.CompatibleRuntimes) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.compatibleRuntimes", OldValue: observed.CompatibleRuntimes, NewValue: desired.CompatibleRuntimes})
	}
	if !slices.Equal(desired.CompatibleArchitectures, observed.CompatibleArchitectures) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.compatibleArchitectures", OldValue: observed.CompatibleArchitectures, NewValue: desired.CompatibleArchitectures})
	}
	if desired.LicenseInfo != observed.LicenseInfo {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.licenseInfo", OldValue: observed.LicenseInfo, NewValue: desired.LicenseInfo})
	}
	desiredPerms := PermissionsSpec{}
	if desired.Permissions != nil {
		desiredPerms = normalizePermissions(*desired.Permissions)
	}
	observedPerms := normalizePermissions(observed.Permissions)
	if !slices.Equal(desiredPerms.AccountIds, observedPerms.AccountIds) || desiredPerms.Public != observedPerms.Public {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.permissions", OldValue: observedPerms, NewValue: desiredPerms})
	}
	return diffs
}
