package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/internal/drivers/dbsubnetgroup"
)

func TestDBSubnetGroupAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewDBSubnetGroupAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"DBSubnetGroup",
		"metadata":{"name":"my-subnet-group"},
		"spec":{
			"region":"us-east-1",
			"description":"My subnet group",
			"subnetIds":["subnet-aaa","subnet-bbb"],
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~my-subnet-group", key)

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(dbsubnetgroup.DBSubnetGroupSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "my-subnet-group", typed.GroupName)
	assert.Equal(t, "My subnet group", typed.Description)
	assert.Equal(t, []string{"subnet-aaa", "subnet-bbb"}, typed.SubnetIds)
	assert.Equal(t, map[string]string{"env": "dev"}, typed.Tags)
}

func TestDBSubnetGroupAdapter_BuildImportKey(t *testing.T) {
	adapter := NewDBSubnetGroupAdapter()
	key, err := adapter.BuildImportKey("us-west-2", "my-group")
	require.NoError(t, err)
	assert.Equal(t, "us-west-2~my-group", key)
}

func TestDBSubnetGroupAdapter_Kind(t *testing.T) {
	adapter := NewDBSubnetGroupAdapter()
	assert.Equal(t, dbsubnetgroup.ServiceName, adapter.Kind())
	assert.Equal(t, dbsubnetgroup.ServiceName, adapter.ServiceName())
}

func TestDBSubnetGroupAdapter_Scope(t *testing.T) {
	adapter := NewDBSubnetGroupAdapter()
	assert.Equal(t, KeyScopeRegion, adapter.Scope())
}

func TestDBSubnetGroupAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewDBSubnetGroupAdapter()
	out, err := adapter.NormalizeOutputs(dbsubnetgroup.DBSubnetGroupOutputs{
		GroupName:         "my-group",
		ARN:               "arn:aws:rds:us-east-1:123:subgrp:my-group",
		VpcId:             "vpc-123",
		SubnetIds:         []string{"subnet-aaa", "subnet-bbb"},
		AvailabilityZones: []string{"us-east-1a", "us-east-1b"},
		Status:            "Complete",
	})
	require.NoError(t, err)
	assert.Equal(t, "my-group", out["groupName"])
	assert.Equal(t, "arn:aws:rds:us-east-1:123:subgrp:my-group", out["arn"])
	assert.Equal(t, "vpc-123", out["vpcId"])
	assert.Equal(t, []string{"subnet-aaa", "subnet-bbb"}, out["subnetIds"])
	assert.Equal(t, []string{"us-east-1a", "us-east-1b"}, out["availabilityZones"])
	assert.Equal(t, "Complete", out["status"])
}
