package auroracluster

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_NoDrift(t *testing.T) {
	spec := AuroraClusterSpec{
		EngineVersion: "8.0", Port: 3306, BackupRetentionPeriod: 7,
		VpcSecurityGroupIds: []string{}, EnabledCloudwatchLogsExports: []string{},
		Tags: map[string]string{"env": "prod"},
	}
	obs := ObservedState{
		EngineVersion: "8.0", Port: 3306, BackupRetentionPeriod: 7,
		VpcSecurityGroupIds: []string{}, EnabledCloudwatchLogsExports: []string{},
		Tags: map[string]string{"env": "prod"},
	}
	assert.False(t, HasDrift(spec, obs))
}

func TestHasDrift_EngineVersionChanged(t *testing.T) {
	spec := AuroraClusterSpec{EngineVersion: "8.1", BackupRetentionPeriod: 7}
	obs := ObservedState{EngineVersion: "8.0", BackupRetentionPeriod: 7}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_PortChanged(t *testing.T) {
	spec := AuroraClusterSpec{EngineVersion: "8.0", Port: 5432, BackupRetentionPeriod: 7}
	obs := ObservedState{EngineVersion: "8.0", Port: 3306, BackupRetentionPeriod: 7}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_SubnetGroupChanged(t *testing.T) {
	spec := AuroraClusterSpec{EngineVersion: "8.0", DBSubnetGroupName: "new-sg", BackupRetentionPeriod: 7}
	obs := ObservedState{EngineVersion: "8.0", DBSubnetGroupName: "old-sg", BackupRetentionPeriod: 7}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_ParameterGroupChanged(t *testing.T) {
	spec := AuroraClusterSpec{EngineVersion: "8.0", DBClusterParameterGroupName: "new-pg", BackupRetentionPeriod: 7}
	obs := ObservedState{EngineVersion: "8.0", DBClusterParameterGroupName: "old-pg", BackupRetentionPeriod: 7}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_SecurityGroupsChanged(t *testing.T) {
	spec := AuroraClusterSpec{EngineVersion: "8.0", BackupRetentionPeriod: 7, VpcSecurityGroupIds: []string{"sg-111", "sg-222"}}
	obs := ObservedState{EngineVersion: "8.0", BackupRetentionPeriod: 7, VpcSecurityGroupIds: []string{"sg-111"}}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_BackupChanged(t *testing.T) {
	spec := AuroraClusterSpec{EngineVersion: "8.0", BackupRetentionPeriod: 14}
	obs := ObservedState{EngineVersion: "8.0", BackupRetentionPeriod: 7}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_DeletionProtectionChanged(t *testing.T) {
	spec := AuroraClusterSpec{EngineVersion: "8.0", DeletionProtection: true, BackupRetentionPeriod: 7}
	obs := ObservedState{EngineVersion: "8.0", DeletionProtection: false, BackupRetentionPeriod: 7}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_CloudwatchLogsChanged(t *testing.T) {
	spec := AuroraClusterSpec{EngineVersion: "8.0", BackupRetentionPeriod: 7, EnabledCloudwatchLogsExports: []string{"audit"}}
	obs := ObservedState{EngineVersion: "8.0", BackupRetentionPeriod: 7, EnabledCloudwatchLogsExports: []string{}}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_TagsChanged(t *testing.T) {
	spec := AuroraClusterSpec{EngineVersion: "8.0", BackupRetentionPeriod: 7, Tags: map[string]string{"env": "staging"}}
	obs := ObservedState{EngineVersion: "8.0", BackupRetentionPeriod: 7, Tags: map[string]string{"env": "prod"}}
	assert.True(t, HasDrift(spec, obs))
}

func TestHasDrift_PraxisTagsIgnored(t *testing.T) {
	spec := AuroraClusterSpec{EngineVersion: "8.0", BackupRetentionPeriod: 7, Tags: map[string]string{}}
	obs := ObservedState{EngineVersion: "8.0", BackupRetentionPeriod: 7, Tags: map[string]string{"praxis:managed": "val"}}
	assert.False(t, HasDrift(spec, obs))
}

func TestComputeFieldDiffs_NoDiff(t *testing.T) {
	spec := AuroraClusterSpec{
		EngineVersion: "8.0", BackupRetentionPeriod: 7,
		VpcSecurityGroupIds: []string{}, EnabledCloudwatchLogsExports: []string{}, Tags: map[string]string{},
	}
	obs := ObservedState{
		EngineVersion: "8.0", BackupRetentionPeriod: 7,
		VpcSecurityGroupIds: []string{}, EnabledCloudwatchLogsExports: []string{}, Tags: map[string]string{},
	}
	assert.Empty(t, ComputeFieldDiffs(spec, obs))
}

func TestComputeFieldDiffs_EngineVersionChange(t *testing.T) {
	spec := AuroraClusterSpec{EngineVersion: "8.1", BackupRetentionPeriod: 7}
	obs := ObservedState{EngineVersion: "8.0", BackupRetentionPeriod: 7}
	diffs := ComputeFieldDiffs(spec, obs)
	found := false
	for _, d := range diffs {
		if d.Path == "spec.engineVersion" {
			found = true
			assert.Equal(t, "8.0", d.OldValue)
			assert.Equal(t, "8.1", d.NewValue)
		}
	}
	assert.True(t, found)
}

func TestComputeFieldDiffs_TagDiffs(t *testing.T) {
	spec := AuroraClusterSpec{EngineVersion: "8.0", BackupRetentionPeriod: 7, Tags: map[string]string{"env": "staging", "new": "tag"}}
	obs := ObservedState{EngineVersion: "8.0", BackupRetentionPeriod: 7, Tags: map[string]string{"env": "prod", "old": "tag"}}
	diffs := ComputeFieldDiffs(spec, obs)
	paths := map[string]bool{}
	for _, d := range diffs {
		paths[d.Path] = true
	}
	assert.True(t, paths["tags.env"])
	assert.True(t, paths["tags.new"])
	assert.True(t, paths["tags.old"])
}

func TestComputeFieldDiffs_ImmutableFieldsAnnotated(t *testing.T) {
	spec := AuroraClusterSpec{EngineVersion: "8.0", Engine: "aurora-postgresql", BackupRetentionPeriod: 7}
	obs := ObservedState{EngineVersion: "8.0", Engine: "aurora-mysql", BackupRetentionPeriod: 7}
	diffs := ComputeFieldDiffs(spec, obs)
	found := false
	for _, d := range diffs {
		if d.Path == "spec.engine (immutable, ignored)" {
			found = true
		}
	}
	assert.True(t, found)
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
