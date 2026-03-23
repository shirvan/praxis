package dbparametergroup

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewDBParameterGroupDriver(nil)
	assert.Equal(t, "DBParameterGroup", drv.ServiceName())
}

func TestValidateSpec_ValidDB(t *testing.T) {
	err := validateSpec(DBParameterGroupSpec{Region: "us-east-1", GroupName: "my-pg", Type: TypeDB, Family: "mysql8.0", Description: "Test"})
	assert.NoError(t, err)
}

func TestValidateSpec_ValidCluster(t *testing.T) {
	err := validateSpec(DBParameterGroupSpec{Region: "us-east-1", GroupName: "my-pg", Type: TypeCluster, Family: "aurora-postgresql16", Description: "Test"})
	assert.NoError(t, err)
}

func TestValidateSpec_MissingRegion(t *testing.T) {
	err := validateSpec(DBParameterGroupSpec{GroupName: "g", Type: TypeDB, Family: "f"})
	assert.ErrorContains(t, err, "region is required")
}

func TestValidateSpec_MissingGroupName(t *testing.T) {
	err := validateSpec(DBParameterGroupSpec{Region: "us-east-1", Type: TypeDB, Family: "f"})
	assert.ErrorContains(t, err, "groupName is required")
}

func TestValidateSpec_InvalidType(t *testing.T) {
	err := validateSpec(DBParameterGroupSpec{Region: "us-east-1", GroupName: "g", Type: "invalid", Family: "f"})
	assert.ErrorContains(t, err, "type must be")
}

func TestValidateSpec_MissingFamily(t *testing.T) {
	err := validateSpec(DBParameterGroupSpec{Region: "us-east-1", GroupName: "g", Type: TypeDB})
	assert.ErrorContains(t, err, "family is required")
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		GroupName:   "my-pg",
		Type:        TypeDB,
		Family:      "mysql8.0",
		Description: "My PG",
		Parameters:  map[string]string{"max_connections": "100"},
		Tags:        map[string]string{"env": "prod", "praxis:managed-key": "key"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, "my-pg", spec.GroupName)
	assert.Equal(t, TypeDB, spec.Type)
	assert.Equal(t, "mysql8.0", spec.Family)
	assert.Equal(t, "My PG", spec.Description)
	assert.Equal(t, map[string]string{"max_connections": "100"}, spec.Parameters)
	assert.Equal(t, map[string]string{"env": "prod"}, spec.Tags)
}

func TestOutputsFromObserved(t *testing.T) {
	obs := ObservedState{GroupName: "my-pg", ARN: "arn:aws:rds:us-east-1:123456:pg:my-pg", Family: "mysql8.0", Type: TypeDB}
	out := outputsFromObserved(obs)
	assert.Equal(t, "my-pg", out.GroupName)
	assert.Equal(t, "arn:aws:rds:us-east-1:123456:pg:my-pg", out.ARN)
	assert.Equal(t, "mysql8.0", out.Family)
	assert.Equal(t, TypeDB, out.Type)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
}

func TestParameterGroupTypeFromKey(t *testing.T) {
	assert.Equal(t, TypeCluster, parameterGroupTypeFromKey("us-east-1~my-cluster-pg"))
	assert.Equal(t, TypeDB, parameterGroupTypeFromKey("us-east-1~my-pg"))
}

func TestApplyDefaults(t *testing.T) {
	spec := applyDefaults(DBParameterGroupSpec{})
	assert.Equal(t, TypeDB, spec.Type)
	assert.NotNil(t, spec.Parameters)
	assert.NotNil(t, spec.Tags)
}
