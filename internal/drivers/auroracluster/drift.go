package auroracluster

import (
	"sort"
	"strings"

	"github.com/shirvan/praxis/internal/drivers"
)

// FieldDiffEntry represents a single field difference between desired and observed state.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift compares desired spec against observed state for all mutable fields:
// engineVersion, port, dbSubnetGroupName, dbClusterParameterGroupName,
// vpcSecurityGroupIds, storageEncrypted, kmsKeyId, backup settings,
// deletionProtection, enabledCloudwatchLogsExports, and tags.
// Immutable fields (engine, masterUsername, databaseName) are NOT checked here.
func HasDrift(desired AuroraClusterSpec, observed ObservedState) bool {
	desired = applyDefaults(desired)
	if desired.EngineVersion != observed.EngineVersion || desired.Port != observed.Port {
		return true
	}
	if desired.DBSubnetGroupName != observed.DBSubnetGroupName {
		return true
	}
	if desired.DBClusterParameterGroupName != "" && desired.DBClusterParameterGroupName != observed.DBClusterParameterGroupName {
		return true
	}
	if !stringSliceEqual(desired.VpcSecurityGroupIds, observed.VpcSecurityGroupIds) {
		return true
	}
	if desired.BackupRetentionPeriod != observed.BackupRetentionPeriod {
		return true
	}
	if desired.PreferredBackupWindow != "" && desired.PreferredBackupWindow != observed.PreferredBackupWindow {
		return true
	}
	if desired.PreferredMaintenanceWindow != "" && desired.PreferredMaintenanceWindow != observed.PreferredMaintenanceWindow {
		return true
	}
	if desired.DeletionProtection != observed.DeletionProtection {
		return true
	}
	if !stringSliceEqual(desired.EnabledCloudwatchLogsExports, observed.EnabledCloudwatchLogsExports) {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs returns a structured list of differences for display.
// Immutable fields (engine, masterUsername, databaseName, storageEncrypted, kmsKeyId)
// are annotated with "(immutable, ignored)" so operators see them but the driver won't correct.
func ComputeFieldDiffs(desired AuroraClusterSpec, observed ObservedState) []FieldDiffEntry {
	desired = applyDefaults(desired)
	var diffs []FieldDiffEntry
	appendIfDifferent := func(path string, oldValue any, newValue any) {
		if oldValue != newValue {
			diffs = append(diffs, FieldDiffEntry{Path: path, OldValue: oldValue, NewValue: newValue})
		}
	}
	appendIfDifferent("spec.engineVersion", observed.EngineVersion, desired.EngineVersion)
	appendIfDifferent("spec.port", observed.Port, desired.Port)
	appendIfDifferent("spec.dbSubnetGroupName", observed.DBSubnetGroupName, desired.DBSubnetGroupName)
	if desired.DBClusterParameterGroupName != "" {
		appendIfDifferent("spec.dbClusterParameterGroupName", observed.DBClusterParameterGroupName, desired.DBClusterParameterGroupName)
	}
	if !stringSliceEqual(desired.VpcSecurityGroupIds, observed.VpcSecurityGroupIds) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.vpcSecurityGroupIds", OldValue: observed.VpcSecurityGroupIds, NewValue: desired.VpcSecurityGroupIds})
	}
	if desired.StorageEncrypted != observed.StorageEncrypted {
		appendIfDifferent("spec.storageEncrypted (immutable, ignored)", observed.StorageEncrypted, desired.StorageEncrypted)
	}
	if desired.KMSKeyId != "" {
		appendIfDifferent("spec.kmsKeyId (immutable, ignored)", observed.KMSKeyId, desired.KMSKeyId)
	}
	appendIfDifferent("spec.backupRetentionPeriod", observed.BackupRetentionPeriod, desired.BackupRetentionPeriod)
	if desired.PreferredBackupWindow != "" {
		appendIfDifferent("spec.preferredBackupWindow", observed.PreferredBackupWindow, desired.PreferredBackupWindow)
	}
	if desired.PreferredMaintenanceWindow != "" {
		appendIfDifferent("spec.preferredMaintenanceWindow", observed.PreferredMaintenanceWindow, desired.PreferredMaintenanceWindow)
	}
	appendIfDifferent("spec.deletionProtection", observed.DeletionProtection, desired.DeletionProtection)
	if !stringSliceEqual(desired.EnabledCloudwatchLogsExports, observed.EnabledCloudwatchLogsExports) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.enabledCloudwatchLogsExports", OldValue: observed.EnabledCloudwatchLogsExports, NewValue: desired.EnabledCloudwatchLogsExports})
	}
	if desired.Engine != "" {
		appendIfDifferent("spec.engine (immutable, ignored)", observed.Engine, desired.Engine)
	}
	if desired.MasterUsername != "" {
		appendIfDifferent("spec.masterUsername (immutable, ignored)", observed.MasterUsername, desired.MasterUsername)
	}
	if desired.DatabaseName != "" {
		appendIfDifferent("spec.databaseName (immutable, ignored)", observed.DatabaseName, desired.DatabaseName)
	}
	for key, value := range drivers.FilterPraxisTags(desired.Tags) {
		if current, ok := drivers.FilterPraxisTags(observed.Tags)[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range drivers.FilterPraxisTags(observed.Tags) {
		if _, ok := drivers.FilterPraxisTags(desired.Tags)[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}

// applyDefaults fills zero-values with Aurora defaults (BackupRetentionPeriod=7)
// and normalizes nil slices to empty for deterministic comparison.
func applyDefaults(spec AuroraClusterSpec) AuroraClusterSpec {
	if spec.BackupRetentionPeriod == 0 {
		spec.BackupRetentionPeriod = 7
	}
	if spec.VpcSecurityGroupIds == nil {
		spec.VpcSecurityGroupIds = []string{}
	}
	if spec.EnabledCloudwatchLogsExports == nil {
		spec.EnabledCloudwatchLogsExports = []string{}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.VpcSecurityGroupIds = normalizeStrings(spec.VpcSecurityGroupIds)
	spec.EnabledCloudwatchLogsExports = normalizeStrings(spec.EnabledCloudwatchLogsExports)
	return spec
}

// normalizeStrings trims whitespace, removes empties, and sorts for deterministic comparison.
func normalizeStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	sort.Strings(normalized)
	return normalized
}

// stringSliceEqual normalizes both slices then compares element-by-element.
func stringSliceEqual(a, b []string) bool {
	aa := normalizeStrings(a)
	bb := normalizeStrings(b)
	if len(aa) != len(bb) {
		return false
	}
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
