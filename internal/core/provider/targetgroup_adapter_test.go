package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/targetgroup"
)

func TestTargetGroupAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewTargetGroupAdapter()
	raw := json.RawMessage(`{
		"apiVersion": "praxis.io/v1",
		"kind": "TargetGroup",
		"metadata": {"name": "api-tg"},
		"spec": {
			"region": "us-east-1",
			"protocol": "HTTP",
			"port": 8080,
			"vpcId": "vpc-123",
			"targetType": "instance",
			"tags": {"env": "dev"}
		}
	}`)
	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	spec, ok := decoded.(targetgroup.TargetGroupSpec)
	require.True(t, ok)
	assert.Equal(t, "api-tg", spec.Name)
	assert.Equal(t, "dev", spec.Tags["env"])
	assert.Equal(t, "api-tg", spec.Tags["Name"])
	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~api-tg", key)
}