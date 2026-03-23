package lambdalayer

import "slices"

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func HasDrift(desired LambdaLayerSpec, observed ObservedState, outputs LambdaLayerOutputs) bool {
	return len(ComputeFieldDiffs(desired, observed, outputs)) > 0
}

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
