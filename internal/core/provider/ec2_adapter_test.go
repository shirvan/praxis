package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/ec2"
)

func TestEC2Adapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewEC2AdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"EC2Instance",
		"metadata":{"name":"web-a"},
		"spec":{
			"region":"us-east-1",
			"imageId":"ami-0123456789abcdef0",
			"instanceType":"t3.micro",
			"subnetId":"subnet-123",
			"securityGroupIds":["sg-123"],
			"monitoring":true,
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~web-a", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(ec2.EC2InstanceSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "ami-0123456789abcdef0", typed.ImageId)
	assert.Equal(t, "t3.micro", typed.InstanceType)
	assert.Equal(t, "subnet-123", typed.SubnetId)
	assert.Equal(t, "web-a", typed.Tags["Name"])
}

func TestEC2Adapter_BuildImportKey(t *testing.T) {
	adapter := NewEC2AdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "i-0abc123")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~i-0abc123", key)
}

func TestEC2Adapter_NormalizeOutputs(t *testing.T) {
	adapter := NewEC2AdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(ec2.EC2InstanceOutputs{
		InstanceId:       "i-123",
		PrivateIpAddress: "10.0.0.12",
		PublicIpAddress:  "54.0.0.12",
		PrivateDnsName:   "ip-10-0-0-12.internal",
		State:            "running",
		SubnetId:         "subnet-1",
		VpcId:            "vpc-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "i-123", out["instanceId"])
	assert.Equal(t, "10.0.0.12", out["privateIpAddress"])
	assert.Equal(t, "54.0.0.12", out["publicIpAddress"])
	assert.Equal(t, "running", out["state"])
}

func TestEC2Adapter_Kind(t *testing.T) {
	adapter := NewEC2AdapterWithAuth(nil)
	assert.Equal(t, ec2.ServiceName, adapter.Kind())
	assert.Equal(t, ec2.ServiceName, adapter.ServiceName())
}

func TestEC2Adapter_Scope(t *testing.T) {
	adapter := NewEC2AdapterWithAuth(nil)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())
}

func TestEC2Adapter_DecodeSpec_MissingRegion(t *testing.T) {
	adapter := NewEC2AdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"EC2Instance",
		"metadata":{"name":"web"},
		"spec":{"imageId":"ami-0123456789abcdef0","instanceType":"t3.micro","subnetId":"subnet-1"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "region")
}

func TestEC2Adapter_DecodeSpec_MissingImageId(t *testing.T) {
	adapter := NewEC2AdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"EC2Instance",
		"metadata":{"name":"web"},
		"spec":{"region":"us-east-1","instanceType":"t3.micro","subnetId":"subnet-1"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "imageId")
}

func TestEC2Adapter_DecodeSpec_MissingSubnetId(t *testing.T) {
	adapter := NewEC2AdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"EC2Instance",
		"metadata":{"name":"web"},
		"spec":{"region":"us-east-1","imageId":"ami-0123456789abcdef0","instanceType":"t3.micro"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subnetId")
}

func TestEC2Adapter_DecodeSpec_SetsNameTag(t *testing.T) {
	adapter := NewEC2AdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"EC2Instance",
		"metadata":{"name":"web-server"},
		"spec":{"region":"us-east-1","imageId":"ami-0123456789abcdef0","instanceType":"t3.micro","subnetId":"subnet-1"}
	}`)
	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed := decoded.(ec2.EC2InstanceSpec)
	assert.Equal(t, "web-server", typed.Tags["Name"])
}

func TestEC2Adapter_BuildKey_MissingName(t *testing.T) {
	adapter := NewEC2AdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"EC2Instance",
		"metadata":{"name":""},
		"spec":{"region":"us-east-1","imageId":"ami-0123456789abcdef0","instanceType":"t3.micro","subnetId":"subnet-1"}
	}`)
	_, err := adapter.BuildKey(raw)
	require.Error(t, err)
}
