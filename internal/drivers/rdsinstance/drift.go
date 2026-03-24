package rdsinstance

import (
	"sort"
	"strings"
)

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func HasDrift(desired RDSInstanceSpec, observed ObservedState) bool {
	desired = applyDefaults(desired)
	if desired.InstanceClass != observed.InstanceClass || desired.EngineVersion != observed.EngineVersion {
		return true
	}
	if desired.AllocatedStorage > 0 && desired.AllocatedStorage != observed.AllocatedStorage {
		return true
	}
	if desired.StorageType != observed.StorageType || desired.IOPS != observed.IOPS || desired.StorageThroughput != observed.StorageThroughput {
		return true
	}
	if desired.StorageEncrypted != observed.StorageEncrypted || desired.KMSKeyId != observed.KMSKeyId {
		return true
	}
	if desired.DBSubnetGroupName != observed.DBSubnetGroupName || desired.ParameterGroupName != observed.ParameterGroupName {
		return true
	}
	if !stringSliceEqual(desired.VpcSecurityGroupIds, observed.VpcSecurityGroupIds) {
		return true
	}
	if desired.MultiAZ != observed.MultiAZ || desired.PubliclyAccessible != observed.PubliclyAccessible {
		return true
	}
	if desired.BackupRetentionPeriod != observed.BackupRetentionPeriod || desired.PreferredBackupWindow != observed.PreferredBackupWindow || desired.PreferredMaintenanceWindow != observed.PreferredMaintenanceWindow {
		return true
	}
	if desired.DeletionProtection != observed.DeletionProtection || desired.AutoMinorVersionUpgrade != observed.AutoMinorVersionUpgrade {
		return true
	}
	if desired.MonitoringInterval != observed.MonitoringInterval || desired.MonitoringRoleArn != observed.MonitoringRoleArn || desired.PerformanceInsightsEnabled != observed.PerformanceInsightsEnabled {
		return true
	}
	return !tagsMatch(desired.Tags, observed.Tags)
}

func ComputeFieldDiffs(desired RDSInstanceSpec, observed ObservedState) []FieldDiffEntry {
	desired = applyDefaults(desired)
	var diffs []FieldDiffEntry
	if desired.InstanceClass != observed.InstanceClass {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.instanceClass", OldValue: observed.InstanceClass, NewValue: desired.InstanceClass})
	}
	if desired.EngineVersion != observed.EngineVersion {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.engineVersion", OldValue: observed.EngineVersion, NewValue: desired.EngineVersion})
	}
	if desired.AllocatedStorage > 0 && desired.AllocatedStorage != observed.AllocatedStorage {
		path := "spec.allocatedStorage"
		if desired.AllocatedStorage < observed.AllocatedStorage {
			path = "spec.allocatedStorage (shrink unsupported)"
		}
		diffs = append(diffs, FieldDiffEntry{Path: path, OldValue: observed.AllocatedStorage, NewValue: desired.AllocatedStorage})
	}
	appendIfDifferent := func(path string, oldValue any, newValue any) {
		if oldValue != newValue {
			diffs = append(diffs, FieldDiffEntry{Path: path, OldValue: oldValue, NewValue: newValue})
		}
	}
	appendIfDifferent("spec.storageType", observed.StorageType, desired.StorageType)
	appendIfDifferent("spec.iops", observed.IOPS, desired.IOPS)
	appendIfDifferent("spec.storageThroughput", observed.StorageThroughput, desired.StorageThroughput)
	appendIfDifferent("spec.storageEncrypted (immutable, ignored)", observed.StorageEncrypted, desired.StorageEncrypted)
	appendIfDifferent("spec.kmsKeyId (immutable, ignored)", observed.KMSKeyId, desired.KMSKeyId)
	appendIfDifferent("spec.dbSubnetGroupName", observed.DBSubnetGroupName, desired.DBSubnetGroupName)
	appendIfDifferent("spec.parameterGroupName", observed.ParameterGroupName, desired.ParameterGroupName)
	if !stringSliceEqual(desired.VpcSecurityGroupIds, observed.VpcSecurityGroupIds) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.vpcSecurityGroupIds", OldValue: observed.VpcSecurityGroupIds, NewValue: desired.VpcSecurityGroupIds})
	}
	appendIfDifferent("spec.multiAZ", observed.MultiAZ, desired.MultiAZ)
	appendIfDifferent("spec.publiclyAccessible", observed.PubliclyAccessible, desired.PubliclyAccessible)
	appendIfDifferent("spec.backupRetentionPeriod", observed.BackupRetentionPeriod, desired.BackupRetentionPeriod)
	appendIfDifferent("spec.preferredBackupWindow", observed.PreferredBackupWindow, desired.PreferredBackupWindow)
	appendIfDifferent("spec.preferredMaintenanceWindow", observed.PreferredMaintenanceWindow, desired.PreferredMaintenanceWindow)
	appendIfDifferent("spec.deletionProtection", observed.DeletionProtection, desired.DeletionProtection)
	appendIfDifferent("spec.autoMinorVersionUpgrade", observed.AutoMinorVersionUpgrade, desired.AutoMinorVersionUpgrade)
	appendIfDifferent("spec.monitoringInterval", observed.MonitoringInterval, desired.MonitoringInterval)
	appendIfDifferent("spec.monitoringRoleArn", observed.MonitoringRoleArn, desired.MonitoringRoleArn)
	appendIfDifferent("spec.performanceInsightsEnabled", observed.PerformanceInsightsEnabled, desired.PerformanceInsightsEnabled)
	appendIfDifferent("spec.engine (immutable, ignored)", observed.Engine, desired.Engine)
	appendIfDifferent("spec.masterUsername (immutable, ignored)", observed.MasterUsername, desired.MasterUsername)
	appendIfDifferent("spec.dbClusterIdentifier (immutable, ignored)", observed.DBClusterIdentifier, desired.DBClusterIdentifier)
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

func applyDefaults(spec RDSInstanceSpec) RDSInstanceSpec {
	if spec.StorageType == "" {
		spec.StorageType = "gp3"
	}
	if spec.BackupRetentionPeriod == 0 && spec.DBClusterIdentifier == "" {
		spec.BackupRetentionPeriod = 7
	}
	if spec.VpcSecurityGroupIds == nil {
		spec.VpcSecurityGroupIds = []string{}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	if spec.MonitoringInterval < 0 {
		spec.MonitoringInterval = 0
	}
	spec.VpcSecurityGroupIds = normalizeStrings(spec.VpcSecurityGroupIds)
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
