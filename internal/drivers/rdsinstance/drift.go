package rdsinstance

import (
	"sort"
	"strings"

	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift compares desired spec against observed AWS state and returns true if
// they differ on any mutable field. Applies defaults before comparison to handle
// zero-value vs. AWS-default mismatches.
//
// Compared mutable fields: instanceClass, engineVersion, allocatedStorage,
// storageType, iops, storageThroughput, dbSubnetGroupName, parameterGroupName,
// vpcSecurityGroupIds, multiAZ, publiclyAccessible, backup settings,
// deletionProtection, autoMinorVersionUpgrade, monitoring, performanceInsights, tags.
//
// Immutable fields (engine, masterUsername, storageEncrypted, kmsKeyId,
// dbClusterIdentifier) are compared in HasDrift but reported as immutable in
// ComputeFieldDiffs — the drift engine needs to know they changed even if
// it cannot correct them.
func HasDrift(desired RDSInstanceSpec, observed ObservedState) bool {
	desired = applyDefaults(desired)
	if desired.DBIdentifier != observed.DBIdentifier || desired.Engine != observed.Engine || desired.StorageEncrypted != observed.StorageEncrypted || desired.DBClusterIdentifier != observed.DBClusterIdentifier {
		return true
	}
	if desired.DBClusterIdentifier == "" && desired.MasterUsername != observed.MasterUsername {
		return true
	}
	if desired.KMSKeyId != "" && desired.KMSKeyId != observed.KMSKeyId {
		return true
	}
	if desired.InstanceClass != observed.InstanceClass || desired.EngineVersion != observed.EngineVersion {
		return true
	}
	if desired.AllocatedStorage > 0 && desired.AllocatedStorage != observed.AllocatedStorage {
		return true
	}
	if desired.StorageType != observed.StorageType || desired.IOPS != observed.IOPS || desired.StorageThroughput != observed.StorageThroughput {
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
	if desired.BackupRetentionPeriod != observed.BackupRetentionPeriod {
		return true
	}
	if desired.PreferredBackupWindow != "" && desired.PreferredBackupWindow != observed.PreferredBackupWindow {
		return true
	}
	if desired.PreferredMaintenanceWindow != "" && desired.PreferredMaintenanceWindow != observed.PreferredMaintenanceWindow {
		return true
	}
	if desired.DeletionProtection != observed.DeletionProtection || desired.AutoMinorVersionUpgrade != observed.AutoMinorVersionUpgrade {
		return true
	}
	if desired.MonitoringInterval != observed.MonitoringInterval || desired.MonitoringRoleArn != observed.MonitoringRoleArn || desired.PerformanceInsightsEnabled != observed.PerformanceInsightsEnabled {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs returns field-level differences for plan output.
// Immutable fields are annotated with "(immutable, ignored)" in the path.
// Storage shrink is annotated with "(shrink unsupported)".
func ComputeFieldDiffs(desired RDSInstanceSpec, observed ObservedState) []drivers.FieldDiff {
	desired = applyDefaults(desired)
	var diffs []drivers.FieldDiff
	if desired.InstanceClass != observed.InstanceClass {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.instanceClass", OldValue: observed.InstanceClass, NewValue: desired.InstanceClass})
	}
	if desired.EngineVersion != observed.EngineVersion {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.engineVersion", OldValue: observed.EngineVersion, NewValue: desired.EngineVersion})
	}
	if desired.AllocatedStorage > 0 && desired.AllocatedStorage != observed.AllocatedStorage {
		path := "spec.allocatedStorage"
		if desired.AllocatedStorage < observed.AllocatedStorage {
			path = "spec.allocatedStorage (shrink unsupported)"
		}
		diffs = append(diffs, drivers.FieldDiff{Path: path, OldValue: observed.AllocatedStorage, NewValue: desired.AllocatedStorage})
	}
	appendIfDifferent := func(path string, oldValue any, newValue any) {
		if oldValue != newValue {
			diffs = append(diffs, drivers.FieldDiff{Path: path, OldValue: oldValue, NewValue: newValue})
		}
	}
	appendIfDifferent("spec.storageType", observed.StorageType, desired.StorageType)
	appendIfDifferent("spec.iops", observed.IOPS, desired.IOPS)
	appendIfDifferent("spec.storageThroughput", observed.StorageThroughput, desired.StorageThroughput)
	appendIfDifferent("spec.storageEncrypted (immutable, ignored)", observed.StorageEncrypted, desired.StorageEncrypted)
	if desired.KMSKeyId != "" {
		appendIfDifferent("spec.kmsKeyId (immutable, ignored)", observed.KMSKeyId, desired.KMSKeyId)
	}
	appendIfDifferent("spec.dbSubnetGroupName", observed.DBSubnetGroupName, desired.DBSubnetGroupName)
	appendIfDifferent("spec.parameterGroupName", observed.ParameterGroupName, desired.ParameterGroupName)
	if !stringSliceEqual(desired.VpcSecurityGroupIds, observed.VpcSecurityGroupIds) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.vpcSecurityGroupIds", OldValue: observed.VpcSecurityGroupIds, NewValue: desired.VpcSecurityGroupIds})
	}
	appendIfDifferent("spec.multiAZ", observed.MultiAZ, desired.MultiAZ)
	appendIfDifferent("spec.publiclyAccessible", observed.PubliclyAccessible, desired.PubliclyAccessible)
	appendIfDifferent("spec.backupRetentionPeriod", observed.BackupRetentionPeriod, desired.BackupRetentionPeriod)
	if desired.PreferredBackupWindow != "" {
		appendIfDifferent("spec.preferredBackupWindow", observed.PreferredBackupWindow, desired.PreferredBackupWindow)
	}
	if desired.PreferredMaintenanceWindow != "" {
		appendIfDifferent("spec.preferredMaintenanceWindow", observed.PreferredMaintenanceWindow, desired.PreferredMaintenanceWindow)
	}
	appendIfDifferent("spec.deletionProtection", observed.DeletionProtection, desired.DeletionProtection)
	appendIfDifferent("spec.autoMinorVersionUpgrade", observed.AutoMinorVersionUpgrade, desired.AutoMinorVersionUpgrade)
	appendIfDifferent("spec.monitoringInterval", observed.MonitoringInterval, desired.MonitoringInterval)
	appendIfDifferent("spec.monitoringRoleArn", observed.MonitoringRoleArn, desired.MonitoringRoleArn)
	appendIfDifferent("spec.performanceInsightsEnabled", observed.PerformanceInsightsEnabled, desired.PerformanceInsightsEnabled)
	if desired.Engine != "" {
		appendIfDifferent("spec.engine (immutable, ignored)", observed.Engine, desired.Engine)
	}
	if desired.MasterUsername != "" {
		appendIfDifferent("spec.masterUsername (immutable, ignored)", observed.MasterUsername, desired.MasterUsername)
	}
	appendIfDifferent("spec.dbClusterIdentifier (immutable, ignored)", observed.DBClusterIdentifier, desired.DBClusterIdentifier)
	for key, value := range drivers.FilterPraxisTags(desired.Tags) {
		if current, ok := drivers.FilterPraxisTags(observed.Tags)[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range drivers.FilterPraxisTags(observed.Tags) {
		if _, ok := drivers.FilterPraxisTags(desired.Tags)[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}

// applyDefaults fills in omitted spec fields with sensible defaults:
// StorageType -> "gp3", BackupRetentionPeriod -> 7 (standalone), nil slices -> empty.
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

// normalizeStrings trims whitespace, removes empty strings, and sorts for
// deterministic comparison of VPC security group IDs and similar lists.
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

// stringSliceEqual compares two string slices after normalization.
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
