package dbsubnetgroup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired := DBSubnetGroupSpec{Description: "Test", SubnetIds: []string{"subnet-1", "subnet-2"}, Tags: map[string]string{"env": "dev"}}
	observed := ObservedState{Description: "Test", SubnetIds: []string{"subnet-1", "subnet-2"}, Tags: map[string]string{"env": "dev"}}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_DescriptionChanged(t *testing.T) {
	desired := DBSubnetGroupSpec{Description: "New desc", SubnetIds: []string{"subnet-1", "subnet-2"}}
	observed := ObservedState{Description: "Old desc", SubnetIds: []string{"subnet-1", "subnet-2"}}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_SubnetsChanged(t *testing.T) {
	desired := DBSubnetGroupSpec{Description: "Test", SubnetIds: []string{"subnet-1", "subnet-3"}}
	observed := ObservedState{Description: "Test", SubnetIds: []string{"subnet-1", "subnet-2"}}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_TagsChanged(t *testing.T) {
	desired := DBSubnetGroupSpec{Description: "Test", SubnetIds: []string{"subnet-1", "subnet-2"}, Tags: map[string]string{"env": "prod"}}
	observed := ObservedState{Description: "Test", SubnetIds: []string{"subnet-1", "subnet-2"}, Tags: map[string]string{"env": "dev"}}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_PraxisTagsIgnored(t *testing.T) {
	desired := DBSubnetGroupSpec{Description: "Test", SubnetIds: []string{"subnet-1", "subnet-2"}, Tags: map[string]string{"env": "dev"}}
	observed := ObservedState{Description: "Test", SubnetIds: []string{"subnet-1", "subnet-2"}, Tags: map[string]string{"env": "dev", "praxis:managed-key": "some-key"}}
	assert.False(t, HasDrift(desired, observed))
}

func TestComputeFieldDiffs_DescriptionChange(t *testing.T) {
	diffs := ComputeFieldDiffs(DBSubnetGroupSpec{Description: "New", SubnetIds: []string{"subnet-1", "subnet-2"}}, ObservedState{Description: "Old", SubnetIds: []string{"subnet-1", "subnet-2"}})
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.description", diffs[0].Path)
	assert.Equal(t, "Old", diffs[0].OldValue)
	assert.Equal(t, "New", diffs[0].NewValue)
}

func TestComputeFieldDiffs_ImmutableGroupName(t *testing.T) {
	diffs := ComputeFieldDiffs(DBSubnetGroupSpec{GroupName: "new-name", Description: "d", SubnetIds: []string{"subnet-1", "subnet-2"}}, ObservedState{GroupName: "old-name", Description: "d", SubnetIds: []string{"subnet-1", "subnet-2"}})
	found := false
	for _, d := range diffs {
		if d.Path == "spec.groupName (immutable, ignored)" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestComputeFieldDiffs_SubnetChange(t *testing.T) {
	diffs := ComputeFieldDiffs(DBSubnetGroupSpec{Description: "d", SubnetIds: []string{"subnet-1", "subnet-3"}}, ObservedState{Description: "d", SubnetIds: []string{"subnet-1", "subnet-2"}})
	found := false
	for _, d := range diffs {
		if d.Path == "spec.subnetIds" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestComputeFieldDiffs_TagAdded(t *testing.T) {
	diffs := ComputeFieldDiffs(DBSubnetGroupSpec{Description: "d", SubnetIds: []string{"subnet-1", "subnet-2"}, Tags: map[string]string{"env": "dev"}}, ObservedState{Description: "d", SubnetIds: []string{"subnet-1", "subnet-2"}})
	found := false
	for _, d := range diffs {
		if d.Path == "tags.env" && d.OldValue == nil {
			found = true
		}
	}
	assert.True(t, found)
}

func TestStringSliceEqual(t *testing.T) {
	assert.True(t, stringSliceEqual([]string{"b", "a"}, []string{"a", "b"}))
	assert.False(t, stringSliceEqual([]string{"a"}, []string{"a", "b"}))
	assert.True(t, stringSliceEqual(nil, nil))
}

func TestTagsMatch(t *testing.T) {
	assert.True(t, tagsMatch(map[string]string{"env": "dev"}, map[string]string{"env": "dev"}))
	assert.False(t, tagsMatch(map[string]string{"env": "dev"}, map[string]string{"env": "prod"}))
	assert.True(t, tagsMatch(map[string]string{"env": "dev"}, map[string]string{"env": "dev", "praxis:key": "val"}))
}

func TestFilterPraxisTags(t *testing.T) {
	result := filterPraxisTags(map[string]string{"env": "dev", "praxis:managed-key": "val"})
	assert.Equal(t, map[string]string{"env": "dev"}, result)
}

func TestNormalizeStrings(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, normalizeStrings([]string{"b", "a"}))
	assert.Nil(t, normalizeStrings(nil))
	assert.Nil(t, normalizeStrings([]string{}))
	assert.Equal(t, []string{"x"}, normalizeStrings([]string{" x ", ""}))
}
