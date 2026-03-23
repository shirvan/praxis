package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/routetable"
)

func TestRouteTableAdapter_BuildKey(t *testing.T) {
	adapter := NewRouteTableAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"RouteTable",
		"metadata":{"name":"public-rt"},
		"spec":{
			"region":"us-east-1",
			"vpcId":"vpc-123",
			"routes":[],
			"associations":[]
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "vpc-123~public-rt", key)
	assert.Equal(t, KeyScopeCustom, adapter.Scope())
}

func TestRouteTableAdapter_DecodeSpecAndNormalizeOutputs(t *testing.T) {
	adapter := NewRouteTableAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"RouteTable",
		"metadata":{"name":"public-rt"},
		"spec":{
			"region":"us-east-1",
			"vpcId":"vpc-123"
		}
	}`)

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(routetable.RouteTableSpec)
	require.True(t, ok)
	assert.Equal(t, "public-rt", typed.Tags["Name"])

	out, err := adapter.NormalizeOutputs(routetable.RouteTableOutputs{RouteTableId: "rtb-123", VpcId: "vpc-123", OwnerId: "123456789012"})
	require.NoError(t, err)
	assert.Equal(t, "rtb-123", out["routeTableId"])
	assert.Equal(t, "vpc-123", out["vpcId"])
	assert.Equal(t, "123456789012", out["ownerId"])
	assert.Contains(t, out, "routes")
	assert.Contains(t, out, "associations")
}

func TestRouteTableAdapter_BuildImportKey(t *testing.T) {
	adapter := NewRouteTableAdapter()
	key, err := adapter.BuildImportKey("us-east-1", "rtb-123")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~rtb-123", key)
}