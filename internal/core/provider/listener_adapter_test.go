package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/listener"
)

func TestListenerAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewListenerAdapterWithAuth(nil)
	doc := json.RawMessage(`{
		"apiVersion": "praxis.io/alpha",
		"kind": "Listener",
		"metadata": {"name": "my-https-listener"},
		"spec": {
			"loadBalancerArn": "arn:aws:elasticloadbalancing:us-east-1:123456:loadbalancer/app/my-alb/abc",
			"port": 443,
			"protocol": "HTTPS",
			"certificateArn": "arn:aws:acm:us-east-1:123456:certificate/xyz",
			"defaultActions": [{"type": "forward", "targetGroupArn": "arn:tg"}]
		}
	}`)
	spec, err := adapter.DecodeSpec(doc)
	require.NoError(t, err)
	assert.NotNil(t, spec)

	key, err := adapter.BuildKey(doc)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~my-https-listener", key)
}

func TestListenerAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewListenerAdapterWithAuth(nil)
	outputs, err := adapter.NormalizeOutputs(listener.ListenerOutputs{
		ListenerArn: "arn:listener", Port: 443, Protocol: "HTTPS",
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"listenerArn": "arn:listener", "port": 443, "protocol": "HTTPS",
	}, outputs)
}

func TestExtractRegionFromLBArn(t *testing.T) {
	assert.Equal(t, "us-east-1", extractRegionFromLBArn("arn:aws:elasticloadbalancing:us-east-1:123456:loadbalancer/app/my-alb/abc"))
	assert.Equal(t, "", extractRegionFromLBArn("invalid"))
}
