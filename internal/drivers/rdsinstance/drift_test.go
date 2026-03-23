package rdsinstance

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_NoDrift(t *testing.T) {
	spec := RDSInstanceSpec{
		InstanceClass: "db.t3.micro", EngineVersion: "8.0", AllocatedStorage: 20,
		StorageType: "gp3", MultiAZ: false, PubliclyAccessible: false,
		BackupRetentionPeriod: 7, DeletionProtection: false,
		VpcSecurityGroupIds: []string{}, Tags: map[string]string{"env": "prod"},
	}
	obs := ObservedState{
		InstanceClass: "db.t3.micro", EngineVersion: "8.0", AllocatedStorage: 20,
		StorageType: "gp3", MultiAZ: false, PubliclyAccessible: false,
		BackupRetentionPeriod: 7, DeletionProtection: false,
		VpcSecurityGroupIds: []string{}, Tags: map[string]string{"env": "prod"},
	}
	assert.False(t, HasDrift(spec, obs))
}

func TestHasDrift_InstanceClassChanged(t *testing.T) {
	spec := RDSInstanceSpec{InstanceClass: "db.r5.large", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7}
	obs := ObservedState{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_EngineVersionChanged(t *testing.T) {
	spec := RDSInstanceSpec{InstanceClass: "db.t3.micro", EngineVersion: "8.1", StorageType: "gp3", BackupRetentionPeriod: 7}
	obs := ObservedState{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_AllocatedStorageChanged(t *testing.T) {
	spec := RDSInstanceSpec{InstanceClass: "db.t3.micro", EngineVersion: "8.0", AllocatedStorage: 50, StorageType: "gp3", BackupRetentionPeriod: 7}
	obs := ObservedState{InstanceClass: "db.t3.micro", EngineVersion: "8.0", AllocatedStorage: 20, StorageType: "gp3", BackupRetentionPeriod: 7}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_StorageTypeChanged(t *testing.T) {
	spec := RDSInstanceSpec{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "io1", BackupRetentionPeriod: 7}
	obs := ObservedState{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_MultiAZChanged(t *testing.T) {
	spec := RDSInstanceSpec{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", MultiAZ: true, BackupRetentionPeriod: 7}
	obs := ObservedState{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", MultiAZ: false, BackupRetentionPeriod: 7}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_DeletionProtectionChanged(t *testing.T) {
	spec := RDSInstanceSpec{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", DeletionProtection: true, BackupRetentionPeriod: 7}
	obs := ObservedState{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", DeletionProtection: false, BackupRetentionPeriod: 7}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_SecurityGroupsChanged(t *testing.T) {
	spec := RDSInstanceSpec{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7, VpcSecurityGroupIds: []string{"sg-111", "sg-222"}}
	obs := ObservedState{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7, VpcSecurityGroupIds: []string{"sg-111"}}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_TagsChanged(t *testing.T) {
	spec := RDSInstanceSpec{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7, Tags: map[string]string{"env": "staging"}}
	obs := ObservedState{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7, Tags: map[string]string{"env": "prod"}}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_PraxisTagsIgnored(t *testing.T) {
	spec := RDSInstanceSpec{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7, Tags: map[string]string{}}
	obs := ObservedState{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7, Tags: map[string]string{"praxis:managed-key": "val"}}
	assert.False(t, HasDrift(spec, obs))
}

func TestHasDrift_MonitoringChanged(t *testing.T) {
	spec := RDSInstanceSpec{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7, MonitoringInterval: 60}
	obs := ObservedState{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7, MonitoringInterval: 0}
	assert.True(t, HasDrift(spec, obs))
}

func TestComputeFieldDiffs_NoDiff(t *testing.T) {
	spec := RDSInstanceSpec{
		InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3",
		BackupRetentionPeriod: 7, VpcSecurityGroupIds: []string{}, Tags: map[string]string{},
	}
	obs := ObservedState{
		InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3",
		BackupRetentionPeriod: 7, VpcSecurityGroupIds: []string{}, Tags: map[string]string{},
	}
	diffs := ComputeFieldDiffs(spec, obs)
	assert.Empty(t, diffs)
}

func TestComputeFieldDiffs_InstanceClassChange(t *testing.T) {
	spec := RDSInstanceSpec{InstanceClass: "db.r5.large", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7}
	obs := ObservedState{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7}
	diffs := ComputeFieldDiffs(spec, obs)
	found := false
	for _, d := range diffs {
		if d.Path == "spec.instanceClass" {
			found = true
			assert.Equal(t, "db.t3.micro", d.OldValue)
			assert.Equal(t, "db.r5.large", d.NewValue)
		}
	}
	assert.True(t, found, "expected instanceClass diff")
}

func TestComputeFieldDiffs_StorageShrink(t *testing.T) {
	spec := RDSInstanceSpec{InstanceClass: "db.t3.micro", EngineVersion: "8.0", AllocatedStorage: 10, StorageType: "gp3", BackupRetentionPeriod: 7}
	obs := ObservedState{InstanceClass: "db.t3.micro", EngineVersion: "8.0", AllocatedStorage: 20, StorageType: "gp3", BackupRetentionPeriod: 7}
	diffs := ComputeFieldDiffs(spec, obs)
	found := false
	for _, d := range diffs {
		if d.Path == "spec.allocatedStorage (shrink unsupported)" {
			found = true
		}
	}
	assert.True(t, found, "expected shrink annotation on allocatedStorage diff")
}

func TestComputeFieldDiffs_TagDiffs(t *testing.T) {
	spec := RDSInstanceSpec{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7, Tags: map[string]string{"env": "staging", "new": "tag"}}
	obs := ObservedState{InstanceClass: "db.t3.micro", EngineVersion: "8.0", StorageType: "gp3", BackupRetentionPeriod: 7, Tags: map[string]string{"env": "prod", "old": "tag"}}
	diffs := ComputeFieldDiffs(spec, obs)
	paths := map[string]bool{}
	for _, d := range diffs {
		paths[d.Path] = true
	}
	assert.True(t, paths["tags.env"])
	assert.True(t, paths["tags.new"])
	assert.True(t, paths["tags.old"])
}

func TestStringSliceEqual(t *testing.T) {
	assert.True(t, stringSliceEqual(nil, nil))
	assert.True(t, stringSliceEqual([]string{}, nil))
	assert.True(t, stringSliceEqual([]string{"a", "b"}, []string{"b", "a"}))
	assert.False(t, stringSliceEqual([]string{"a"}, []string{"a", "b"}))
}

func TestTagsMatch(t *testing.T) {
	assert.True(t, tagsMatch(nil, nil))
	assert.True(t, tagsMatch(map[string]string{"k": "v"}, map[string]string{"k": "v"}))
	assert.False(t, tagsMatch(map[string]string{"k": "v"}, map[string]string{"k": "x"}))
	assert.True(t, tagsMatch(map[string]string{}, map[string]string{"praxis:key": "val"}))
}

func TestFilterPraxisTags(t *testing.T) {
	result := filterPraxisTags(map[string]string{"env": "prod", "praxis:managed": "key", "team": "backend"})
	assert.Equal(t, map[string]string{"env": "prod", "team": "backend"}, result)
}

func TestNormalizeStrings(t *testing.T) {
	assert.Equal(t, []string{}, normalizeStrings(nil))
	assert.Equal(t, []string{"a", "b"}, normalizeStrings([]string{" b ", " a "}))
	assert.Equal(t, []string{}, normalizeStrings([]string{" ", ""}))
}
