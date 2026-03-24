package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/alb"
)

func TestALBAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewALBAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion": "praxis.io/v1",
		"kind": "ALB",
		"metadata": {"name": "my-alb"},
		"spec": {
			"region": "us-east-1",
			"scheme": "internet-facing",
			"subnets": ["subnet-1", "subnet-2"],
			"securityGroups": ["sg-1"],
			"tags": {"env": "dev"}
		}
	}`)
	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	spec, ok := decoded.(alb.ALBSpec)
	require.True(t, ok)
	assert.Equal(t, "my-alb", spec.Name)
	assert.Equal(t, "dev", spec.Tags["env"])
	assert.Equal(t, "my-alb", spec.Tags["Name"])
	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~my-alb", key)
}
