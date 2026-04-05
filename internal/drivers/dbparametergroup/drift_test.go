package dbparametergroup

import (
	"github.com/shirvan/praxis/internal/drivers"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired := DBParameterGroupSpec{Parameters: map[string]string{"max_connections": "100"}, Tags: map[string]string{"env": "dev"}}
	observed := ObservedState{Parameters: map[string]string{"max_connections": "100"}, Tags: map[string]string{"env": "dev"}}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_ParameterChanged(t *testing.T) {
	desired := DBParameterGroupSpec{Parameters: map[string]string{"max_connections": "200"}}
	observed := ObservedState{Parameters: map[string]string{"max_connections": "100"}}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_ParameterAdded(t *testing.T) {
	desired := DBParameterGroupSpec{Parameters: map[string]string{"max_connections": "100", "wait_timeout": "300"}}
	observed := ObservedState{Parameters: map[string]string{"max_connections": "100"}}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_ParameterRemoved(t *testing.T) {
	desired := DBParameterGroupSpec{Parameters: map[string]string{}}
	observed := ObservedState{Parameters: map[string]string{"max_connections": "100"}}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_TagsChanged(t *testing.T) {
	desired := DBParameterGroupSpec{Tags: map[string]string{"env": "prod"}}
	observed := ObservedState{Tags: map[string]string{"env": "dev"}}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_PraxisTagsIgnored(t *testing.T) {
	desired := DBParameterGroupSpec{Tags: map[string]string{"env": "dev"}}
	observed := ObservedState{Tags: map[string]string{"env": "dev", "praxis:key": "val"}}
	assert.False(t, HasDrift(desired, observed))
}

func TestComputeFieldDiffs_ImmutableGroupName(t *testing.T) {
	diffs := ComputeFieldDiffs(DBParameterGroupSpec{GroupName: "new", Type: TypeDB, Family: "f"}, ObservedState{GroupName: "old", Type: TypeDB, Family: "f"})
	found := false
	for _, d := range diffs {
		if d.Path == "spec.groupName (immutable, ignored)" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestComputeFieldDiffs_ImmutableType(t *testing.T) {
	diffs := ComputeFieldDiffs(DBParameterGroupSpec{GroupName: "g", Type: TypeCluster, Family: "f"}, ObservedState{GroupName: "g", Type: TypeDB, Family: "f"})
	found := false
	for _, d := range diffs {
		if d.Path == "spec.type (immutable, ignored)" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestComputeFieldDiffs_ImmutableFamily(t *testing.T) {
	diffs := ComputeFieldDiffs(DBParameterGroupSpec{GroupName: "g", Type: TypeDB, Family: "mysql8.0"}, ObservedState{GroupName: "g", Type: TypeDB, Family: "mysql5.7"})
	found := false
	for _, d := range diffs {
		if d.Path == "spec.family (immutable, ignored)" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestComputeFieldDiffs_ParameterDiffs(t *testing.T) {
	diffs := ComputeFieldDiffs(
		DBParameterGroupSpec{Parameters: map[string]string{"max_connections": "200", "new_param": "val"}},
		ObservedState{Parameters: map[string]string{"max_connections": "100", "old_param": "val"}},
	)
	paths := map[string]bool{}
	for _, d := range diffs {
		paths[d.Path] = true
	}
	assert.True(t, paths["spec.parameters.max_connections"])
	assert.True(t, paths["spec.parameters.new_param"])
	assert.True(t, paths["spec.parameters.old_param"])
}

func TestParametersEqual(t *testing.T) {
	assert.True(t, parametersEqual(map[string]string{"a": "1"}, map[string]string{"a": "1"}))
	assert.False(t, parametersEqual(map[string]string{"a": "1"}, map[string]string{"a": "2"}))
	assert.False(t, parametersEqual(map[string]string{"a": "1"}, map[string]string{}))
}

func TestFilterPraxisTags(t *testing.T) {
	result := drivers.FilterPraxisTags(map[string]string{"env": "dev", "praxis:key": "val"})
	assert.Equal(t, map[string]string{"env": "dev"}, result)
	assert.Equal(t, map[string]string{}, drivers.FilterPraxisTags(nil))
}
