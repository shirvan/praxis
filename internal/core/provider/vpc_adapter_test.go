package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/vpc"
)

func TestVPCAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewVPCAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"VPC",
		"metadata":{"name":"main-vpc"},
		"spec":{
			"region":"us-east-1",
			"cidrBlock":"10.0.0.0/16",
			"enableDnsHostnames":true,
			"enableDnsSupport":true,
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~main-vpc", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(vpc.VPCSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "10.0.0.0/16", typed.CidrBlock)
	assert.True(t, typed.EnableDnsHostnames)
	assert.True(t, typed.EnableDnsSupport)
	assert.Equal(t, "default", typed.InstanceTenancy)
	assert.Equal(t, "main-vpc", typed.Tags["Name"])
}

func TestVPCAdapter_BuildImportKey(t *testing.T) {
	adapter := NewVPCAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "vpc-0abc123")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~vpc-0abc123", key)
}

func TestVPCAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewVPCAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(vpc.VPCOutputs{
		VpcId:              "vpc-123",
		ARN:                "arn:aws:ec2:us-east-1:123456789012:vpc/vpc-123",
		CidrBlock:          "10.0.0.0/16",
		State:              "available",
		EnableDnsHostnames: true,
		EnableDnsSupport:   true,
		InstanceTenancy:    "default",
		OwnerId:            "123456789012",
		DhcpOptionsId:      "dopt-123",
		IsDefault:          false,
	})
	require.NoError(t, err)
	assert.Equal(t, "vpc-123", out["vpcId"])
	assert.Equal(t, "10.0.0.0/16", out["cidrBlock"])
	assert.Equal(t, "available", out["state"])
	assert.Equal(t, true, out["enableDnsHostnames"])
	assert.Equal(t, true, out["enableDnsSupport"])
	assert.Equal(t, "default", out["instanceTenancy"])
	assert.Equal(t, "123456789012", out["ownerId"])
	assert.Equal(t, "dopt-123", out["dhcpOptionsId"])
	assert.Equal(t, false, out["isDefault"])
	assert.Equal(t, "arn:aws:ec2:us-east-1:123456789012:vpc/vpc-123", out["arn"])
}

func TestVPCAdapter_NormalizeOutputs_NoARN(t *testing.T) {
	adapter := NewVPCAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(vpc.VPCOutputs{
		VpcId: "vpc-123",
	})
	require.NoError(t, err)
	_, hasARN := out["arn"]
	assert.False(t, hasARN, "arn should be omitted when empty")
}

func TestVPCAdapter_Kind(t *testing.T) {
	adapter := NewVPCAdapterWithAuth(nil)
	assert.Equal(t, vpc.ServiceName, adapter.Kind())
	assert.Equal(t, vpc.ServiceName, adapter.ServiceName())
}

func TestVPCAdapter_Scope(t *testing.T) {
	adapter := NewVPCAdapterWithAuth(nil)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())
}

func TestVPCAdapter_DecodeSpec_MissingRegion(t *testing.T) {
	adapter := NewVPCAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"VPC",
		"metadata":{"name":"my-vpc"},
		"spec":{"cidrBlock":"10.0.0.0/16"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "region")
}

func TestVPCAdapter_DecodeSpec_MissingCidrBlock(t *testing.T) {
	adapter := NewVPCAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"VPC",
		"metadata":{"name":"my-vpc"},
		"spec":{"region":"us-east-1"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cidrBlock")
}

func TestVPCAdapter_DecodeSpec_MissingName(t *testing.T) {
	adapter := NewVPCAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"VPC",
		"metadata":{"name":""},
		"spec":{"region":"us-east-1","cidrBlock":"10.0.0.0/16"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}

func TestVPCAdapter_DecodeSpec_SetsNameTag(t *testing.T) {
	adapter := NewVPCAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"VPC",
		"metadata":{"name":"prod-vpc"},
		"spec":{"region":"us-east-1","cidrBlock":"10.0.0.0/16"}
	}`)
	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed := decoded.(vpc.VPCSpec)
	assert.Equal(t, "prod-vpc", typed.Tags["Name"])
}

func TestVPCAdapter_DecodeSpec_DefaultsTenancy(t *testing.T) {
	adapter := NewVPCAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"VPC",
		"metadata":{"name":"my-vpc"},
		"spec":{"region":"us-east-1","cidrBlock":"10.0.0.0/16"}
	}`)
	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed := decoded.(vpc.VPCSpec)
	assert.Equal(t, "default", typed.InstanceTenancy)
}

func TestVPCAdapter_BuildKey_MissingName(t *testing.T) {
	adapter := NewVPCAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"VPC",
		"metadata":{"name":""},
		"spec":{"region":"us-east-1","cidrBlock":"10.0.0.0/16"}
	}`)
	_, err := adapter.BuildKey(raw)
	require.Error(t, err)
}
