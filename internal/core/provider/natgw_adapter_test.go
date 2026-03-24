package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/natgw"
)

func TestNATGatewayAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewNATGatewayAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"NATGateway",
		"metadata":{"name":"nat-a"},
		"spec":{"region":"us-east-1","subnetId":"subnet-123","allocationId":"eipalloc-123","tags":{"env":"dev"}}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~nat-a", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(natgw.NATGatewaySpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "subnet-123", typed.SubnetId)
	assert.Equal(t, "public", typed.ConnectivityType)
	assert.Equal(t, "eipalloc-123", typed.AllocationId)
	assert.Equal(t, "nat-a", typed.Tags["Name"])
	assert.Equal(t, "dev", typed.Tags["env"])
}

func TestNATGatewayAdapter_BuildImportKey(t *testing.T) {
	adapter := NewNATGatewayAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "nat-123")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~nat-123", key)
}

func TestNATGatewayAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewNATGatewayAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(natgw.NATGatewayOutputs{
		NatGatewayId:       "nat-123",
		SubnetId:           "subnet-123",
		VpcId:              "vpc-123",
		ConnectivityType:   "public",
		State:              "available",
		PublicIp:           "203.0.113.10",
		PrivateIp:          "10.0.1.10",
		AllocationId:       "eipalloc-123",
		NetworkInterfaceId: "eni-123",
	})
	require.NoError(t, err)
	assert.Equal(t, "nat-123", out["natGatewayId"])
	assert.Equal(t, "subnet-123", out["subnetId"])
	assert.Equal(t, "vpc-123", out["vpcId"])
	assert.Equal(t, "public", out["connectivityType"])
	assert.Equal(t, "available", out["state"])
	assert.Equal(t, "203.0.113.10", out["publicIp"])
	assert.Equal(t, "10.0.1.10", out["privateIp"])
	assert.Equal(t, "eipalloc-123", out["allocationId"])
	assert.Equal(t, "eni-123", out["networkInterfaceId"])
}
