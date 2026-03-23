package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/route53record"
)

func TestRoute53RecordAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewRoute53RecordAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"Route53Record",
		"metadata":{"name":"www-record"},
		"spec":{
			"hostedZoneId":"/hostedzone/Z123456789",
			"name":"WWW.Example.COM.",
			"type":"a",
			"ttl":60,
			"resourceRecords":["192.0.2.10"],
			"setIdentifier":"blue"
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "Z123456789~www.example.com~A~blue", key)
	assert.Equal(t, KeyScopeCustom, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(route53record.RecordSpec)
	require.True(t, ok)
	assert.Equal(t, "Z123456789", typed.HostedZoneId)
	assert.Equal(t, "www.example.com", typed.Name)
	assert.Equal(t, "A", typed.Type)
	assert.Equal(t, "blue", typed.SetIdentifier)
}

func TestRoute53RecordAdapter_NormalizeOutputsAndImportKey(t *testing.T) {
	adapter := NewRoute53RecordAdapter()
	out, err := adapter.NormalizeOutputs(route53record.RecordOutputs{
		HostedZoneId:  "Z123456789",
		FQDN:          "www.example.com.",
		Type:          "A",
		SetIdentifier: "blue",
	})
	require.NoError(t, err)
	assert.Equal(t, "Z123456789", out["hostedZoneId"])
	assert.Equal(t, "www.example.com.", out["fqdn"])
	assert.Equal(t, "A", out["type"])
	assert.Equal(t, "blue", out["setIdentifier"])

	key, err := adapter.BuildImportKey("us-east-1", "Z123456789/www.example.com./A/blue")
	require.NoError(t, err)
	assert.Equal(t, "Z123456789~www.example.com.~A~blue", key)
}
