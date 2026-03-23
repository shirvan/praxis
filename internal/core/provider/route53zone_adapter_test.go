package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/internal/drivers/route53zone"
)

func TestRoute53HostedZoneAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewRoute53HostedZoneAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"Route53HostedZone",
		"metadata":{"name":"Example.COM."},
		"spec":{
			"comment":"public zone",
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "example.com", key)
	assert.Equal(t, KeyScopeGlobal, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(route53zone.HostedZoneSpec)
	require.True(t, ok)
	assert.Equal(t, "example.com", typed.Name)
	assert.Equal(t, "public zone", typed.Comment)
	assert.Equal(t, "dev", typed.Tags["env"])
}

func TestRoute53HostedZoneAdapter_NormalizeOutputsAndImportKey(t *testing.T) {
	adapter := NewRoute53HostedZoneAdapter()
	out, err := adapter.NormalizeOutputs(route53zone.HostedZoneOutputs{
		HostedZoneId: "Z123456789",
		Name:         "example.com",
		NameServers:  []string{"ns-1.example.net", "ns-2.example.net"},
		IsPrivate:    false,
		RecordCount:  5,
	})
	require.NoError(t, err)
	assert.Equal(t, "Z123456789", out["hostedZoneId"])
	assert.Equal(t, "example.com", out["name"])
	assert.Equal(t, int64(5), out["recordCount"])

	key, err := adapter.BuildImportKey("us-east-1", "Z123456789")
	require.NoError(t, err)
	assert.Equal(t, "Z123456789", key)
}
