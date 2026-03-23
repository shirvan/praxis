package auroracluster

import (
	"sort"
	"strings"
)

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func HasDrift(desired AuroraClusterSpec, observed ObservedState) bool {
	desired = applyDefaults(desired)
	if desired.EngineVersion != observed.EngineVersion || desired.Port != observed.Port {
		return true
	}
	if desired.DBSubnetGroupName != observed.DBSubnetGroupName || desired.DBClusterParameterGroupName != observed.DBClusterParameterGroupName {
		return true
	}
	if !stringSliceEqual(desired.VpcSecurityGroupIds, observed.VpcSecurityGroupIds) {
		return true
	}
	if desired.StorageEncrypted != observed.StorageEncrypted || desired.KMSKeyId != observed.KMSKeyId {
		return true
	}
	if desired.BackupRetentionPeriod != observed.BackupRetentionPeriod || desired.PreferredBackupWindow != observed.PreferredBackupWindow || desired.PreferredMaintenanceWindow != observed.PreferredMaintenanceWindow {
		return true
	}
	if desired.DeletionProtection != observed.DeletionProtection {
		return true
	}
	if !stringSliceEqual(desired.EnabledCloudwatchLogsExports, observed.EnabledCloudwatchLogsExports) {
		return true
	}
	return !tagsMatch(desired.Tags, observed.Tags)
}

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
	appendIfDifferent("spec.dbClusterParameterGroupName", observed.DBClusterParameterGroupName, desired.DBClusterParameterGroupName)
	if !stringSliceEqual(desired.VpcSecurityGroupIds, observed.VpcSecurityGroupIds) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.vpcSecurityGroupIds", OldValue: observed.VpcSecurityGroupIds, NewValue: desired.VpcSecurityGroupIds})
	}
	appendIfDifferent("spec.storageEncrypted (immutable, ignored)", observed.StorageEncrypted, desired.StorageEncrypted)
	appendIfDifferent("spec.kmsKeyId (immutable, ignored)", observed.KMSKeyId, desired.KMSKeyId)
	appendIfDifferent("spec.backupRetentionPeriod", observed.BackupRetentionPeriod, desired.BackupRetentionPeriod)
	appendIfDifferent("spec.preferredBackupWindow", observed.PreferredBackupWindow, desired.PreferredBackupWindow)
	appendIfDifferent("spec.preferredMaintenanceWindow", observed.PreferredMaintenanceWindow, desired.PreferredMaintenanceWindow)
	appendIfDifferent("spec.deletionProtection", observed.DeletionProtection, desired.DeletionProtection)
	if !stringSliceEqual(desired.EnabledCloudwatchLogsExports, observed.EnabledCloudwatchLogsExports) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.enabledCloudwatchLogsExports", OldValue: observed.EnabledCloudwatchLogsExports, NewValue: desired.EnabledCloudwatchLogsExports})
	}
	appendIfDifferent("spec.engine (immutable, ignored)", observed.Engine, desired.Engine)
	appendIfDifferent("spec.masterUsername (immutable, ignored)", observed.MasterUsername, desired.MasterUsername)
	appendIfDifferent("spec.databaseName (immutable, ignored)", observed.DatabaseName, desired.DatabaseName)
	for key, value := range filterPraxisTags(desired.Tags) {
		if current, ok := filterPraxisTags(observed.Tags)[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range filterPraxisTags(observed.Tags) {
		if _, ok := filterPraxisTags(desired.Tags)[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}

func applyDefaults(spec AuroraClusterSpec) AuroraClusterSpec {
	if spec.BackupRetentionPeriod == 0 {
		spec.BackupRetentionPeriod = 7
	}
	if spec.StorageEncrypted == false {
		// retain explicit false if set
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

func filterPraxisTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(tags))
	for key, value := range tags {
		if !strings.HasPrefix(key, "praxis:") {
			out[key] = value
		}
	}
	return out
}
