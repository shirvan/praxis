package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/internal/drivers/subnet"
)

func TestSubnetAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewSubnetAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"Subnet",
		"metadata":{"name":"public-a"},
		"spec":{
			"region":"us-east-1",
			"vpcId":"vpc-123",
			"cidrBlock":"10.0.1.0/24",
			"availabilityZone":"us-east-1a",
			"mapPublicIpOnLaunch":true,
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "vpc-123~public-a", key)
	assert.Equal(t, KeyScopeCustom, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(subnet.SubnetSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "vpc-123", typed.VpcId)
	assert.Equal(t, "10.0.1.0/24", typed.CidrBlock)
	assert.Equal(t, "us-east-1a", typed.AvailabilityZone)
	assert.True(t, typed.MapPublicIpOnLaunch)
	assert.Equal(t, "public-a", typed.Tags["Name"])
}

func TestSubnetAdapter_BuildImportKey(t *testing.T) {
	adapter := NewSubnetAdapter()
	key, err := adapter.BuildImportKey("us-east-1", "subnet-0abc123")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~subnet-0abc123", key)
}

func TestSubnetAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewSubnetAdapter()
	out, err := adapter.NormalizeOutputs(subnet.SubnetOutputs{
		SubnetId:            "subnet-123",
		ARN:                 "arn:aws:ec2:us-east-1:123456789012:subnet/subnet-123",
		VpcId:               "vpc-123",
		CidrBlock:           "10.0.1.0/24",
		AvailabilityZone:    "us-east-1a",
		AvailabilityZoneId:  "use1-az1",
		MapPublicIpOnLaunch: true,
		State:               "available",
		OwnerId:             "123456789012",
		AvailableIpCount:    251,
	})
	require.NoError(t, err)
	assert.Equal(t, "subnet-123", out["subnetId"])
	assert.Equal(t, "vpc-123", out["vpcId"])
	assert.Equal(t, "10.0.1.0/24", out["cidrBlock"])
	assert.Equal(t, true, out["mapPublicIpOnLaunch"])
	assert.Equal(t, 251, out["availableIpCount"])
	assert.Equal(t, "arn:aws:ec2:us-east-1:123456789012:subnet/subnet-123", out["arn"])
}

func TestSubnetAdapter_DecodeSpec_MissingVpcID(t *testing.T) {
	adapter := NewSubnetAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"Subnet",
		"metadata":{"name":"public-a"},
		"spec":{"region":"us-east-1","cidrBlock":"10.0.1.0/24","availabilityZone":"us-east-1a"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vpcId")
}

func TestSubnetAdapter_DecodeSpec_MissingAvailabilityZone(t *testing.T) {
	adapter := NewSubnetAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"Subnet",
		"metadata":{"name":"public-a"},
		"spec":{"region":"us-east-1","vpcId":"vpc-123","cidrBlock":"10.0.1.0/24"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "availabilityZone")
}
