package dbsubnetgroup

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewDBSubnetGroupDriver(nil)
	assert.Equal(t, "DBSubnetGroup", drv.ServiceName())
}

func TestValidateSpec_Valid(t *testing.T) {
	err := validateSpec(DBSubnetGroupSpec{
		Region:      "us-east-1",
		GroupName:   "my-subnet-group",
		Description: "Test group",
		SubnetIds:   []string{"subnet-1", "subnet-2"},
	})
	assert.NoError(t, err)
}

func TestValidateSpec_MissingRegion(t *testing.T) {
	err := validateSpec(DBSubnetGroupSpec{GroupName: "g", Description: "d", SubnetIds: []string{"a", "b"}})
	assert.ErrorContains(t, err, "region is required")
}

func TestValidateSpec_MissingGroupName(t *testing.T) {
	err := validateSpec(DBSubnetGroupSpec{Region: "us-east-1", Description: "d", SubnetIds: []string{"a", "b"}})
	assert.ErrorContains(t, err, "groupName is required")
}

func TestValidateSpec_MissingDescription(t *testing.T) {
	err := validateSpec(DBSubnetGroupSpec{Region: "us-east-1", GroupName: "g", SubnetIds: []string{"a", "b"}})
	assert.ErrorContains(t, err, "description is required")
}

func TestValidateSpec_TooFewSubnets(t *testing.T) {
	err := validateSpec(DBSubnetGroupSpec{Region: "us-east-1", GroupName: "g", Description: "d", SubnetIds: []string{"subnet-1"}})
	assert.ErrorContains(t, err, "at least 2 subnets")
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		GroupName:   "my-group",
		Description: "My description",
		SubnetIds:   []string{"subnet-b", "subnet-a"},
		Tags:        map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~my-group"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, "my-group", spec.GroupName)
	assert.Equal(t, "My description", spec.Description)
	assert.Equal(t, []string{"subnet-a", "subnet-b"}, spec.SubnetIds)
	assert.Equal(t, map[string]string{"env": "prod"}, spec.Tags)
}

func TestOutputsFromObserved(t *testing.T) {
	obs := ObservedState{
		GroupName:         "my-group",
		ARN:               "arn:aws:rds:us-east-1:123456:subgrp:my-group",
		VpcId:             "vpc-123",
		SubnetIds:         []string{"subnet-1", "subnet-2"},
		AvailabilityZones: []string{"us-east-1a", "us-east-1b"},
		Status:            "Complete",
	}
	out := outputsFromObserved(obs)
	assert.Equal(t, "my-group", out.GroupName)
	assert.Equal(t, "arn:aws:rds:us-east-1:123456:subgrp:my-group", out.ARN)
	assert.Equal(t, "vpc-123", out.VpcId)
	assert.Equal(t, []string{"subnet-1", "subnet-2"}, out.SubnetIds)
	assert.Equal(t, []string{"us-east-1a", "us-east-1b"}, out.AvailabilityZones)
	assert.Equal(t, "Complete", out.Status)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
}

func TestApplyDefaults(t *testing.T) {
	spec := applyDefaults(DBSubnetGroupSpec{SubnetIds: []string{"b", "a"}})
	assert.Equal(t, []string{"a", "b"}, spec.SubnetIds)
	assert.NotNil(t, spec.Tags)
}
