package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/ebs"
)

func TestEBSAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewEBSAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"EBSVolume",
		"metadata":{"name":"data-vol"},
		"spec":{
			"region":"us-east-1",
			"availabilityZone":"us-east-1a",
			"volumeType":"gp3",
			"sizeGiB":20,
			"encrypted":true,
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~data-vol", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(ebs.EBSVolumeSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "us-east-1a", typed.AvailabilityZone)
	assert.Equal(t, "gp3", typed.VolumeType)
	assert.Equal(t, int32(20), typed.SizeGiB)
	assert.Equal(t, "data-vol", typed.Tags["Name"])
}

func TestEBSAdapter_BuildImportKey(t *testing.T) {
	adapter := NewEBSAdapter()
	key, err := adapter.BuildImportKey("us-east-1", "vol-0abc123")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~vol-0abc123", key)
}

func TestEBSAdapter_Kind(t *testing.T) {
	adapter := NewEBSAdapter()
	assert.Equal(t, ebs.ServiceName, adapter.Kind())
	assert.Equal(t, ebs.ServiceName, adapter.ServiceName())
}

func TestEBSAdapter_Scope(t *testing.T) {
	adapter := NewEBSAdapter()
	assert.Equal(t, KeyScopeRegion, adapter.Scope())
}

func TestEBSAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewEBSAdapter()
	out, err := adapter.NormalizeOutputs(ebs.EBSVolumeOutputs{
		VolumeId:         "vol-123",
		AvailabilityZone: "us-east-1a",
		State:            "available",
		SizeGiB:          100,
		VolumeType:       "gp3",
		Encrypted:        true,
	})
	require.NoError(t, err)
	assert.Equal(t, "vol-123", out["volumeId"])
	assert.Equal(t, "us-east-1a", out["availabilityZone"])
	assert.Equal(t, "available", out["state"])
	assert.Equal(t, int32(100), out["sizeGiB"])
	assert.Equal(t, "gp3", out["volumeType"])
	assert.Equal(t, true, out["encrypted"])
}
