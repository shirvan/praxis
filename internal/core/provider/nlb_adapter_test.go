package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNLBAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewNLBAdapterWithRegistry(nil)
	doc := json.RawMessage(`{
		"kind": "NLB",
		"metadata": {"name": "my-nlb"},
		"spec": {"region": "us-east-1", "subnets": ["subnet-1"]}
	}`)
	spec, err := adapter.DecodeSpec(doc)
	require.NoError(t, err)
	assert.NotNil(t, spec)

	key, err := adapter.BuildKey(doc)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~my-nlb", key)
}
