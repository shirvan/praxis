package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/vpcpeering"
)

func TestVPCPeeringAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewVPCPeeringAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"VPCPeeringConnection",
		"metadata":{"name":"app-to-shared"},
		"spec":{
			"region":"us-east-1",
			"requesterVpcId":"vpc-aaa",
			"accepterVpcId":"vpc-bbb",
			"requesterOptions":{"allowDnsResolutionFromRemoteVpc":true}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~app-to-shared", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(vpcpeering.VPCPeeringSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "vpc-aaa", typed.RequesterVpcId)
	assert.Equal(t, "vpc-bbb", typed.AccepterVpcId)
	assert.True(t, typed.AutoAccept)
	assert.Equal(t, "app-to-shared", typed.Tags["Name"])
	assert.True(t, typed.RequesterOptions.AllowDnsResolutionFromRemoteVpc)
}

func TestVPCPeeringAdapter_BuildImportKey(t *testing.T) {
	adapter := NewVPCPeeringAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "pcx-123")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~pcx-123", key)
}

func TestVPCPeeringAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewVPCPeeringAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(vpcpeering.VPCPeeringOutputs{
		VpcPeeringConnectionId: "pcx-123",
		RequesterVpcId:         "vpc-a",
		AccepterVpcId:          "vpc-b",
		RequesterCidrBlock:     "10.0.0.0/16",
		AccepterCidrBlock:      "10.1.0.0/16",
		Status:                 "active",
		RequesterOwnerId:       "111111111111",
		AccepterOwnerId:        "111111111111",
	})
	require.NoError(t, err)
	assert.Equal(t, "pcx-123", out["vpcPeeringConnectionId"])
	assert.Equal(t, "vpc-a", out["requesterVpcId"])
	assert.Equal(t, "vpc-b", out["accepterVpcId"])
	assert.Equal(t, "active", out["status"])
}

func TestVPCPeeringAdapter_DecodeSpec_MissingFields(t *testing.T) {
	adapter := NewVPCPeeringAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"VPCPeeringConnection",
		"metadata":{"name":"peer"},
		"spec":{"region":"us-east-1","requesterVpcId":"vpc-a"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepterVpcId")
}
