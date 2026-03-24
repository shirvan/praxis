package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListenerRuleAdapter_BuildKey(t *testing.T) {
	adapter := NewListenerRuleAdapterWithAuth(nil)
	doc := map[string]any{
		"apiVersion": "praxis.io/v1",
		"kind":       "ListenerRule",
		"metadata":   map[string]any{"name": "api-path-rule"},
		"spec": map[string]any{
			"listenerArn": "arn:aws:elasticloadbalancing:us-west-2:123456789012:listener/app/my-alb/50dc6c495c0c9188/f2f7dc8efc522ab2",
			"priority":    10,
			"conditions":  []any{map[string]any{"field": "path-pattern", "values": []any{"/api/*"}}},
			"actions":     []any{map[string]any{"type": "forward", "targetGroupArn": "arn:tg"}},
		},
	}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-west-2~api-path-rule", key)
}

func TestListenerRuleAdapter_DecodeSpec(t *testing.T) {
	adapter := NewListenerRuleAdapterWithAuth(nil)
	doc := map[string]any{
		"apiVersion": "praxis.io/v1",
		"kind":       "ListenerRule",
		"metadata":   map[string]any{"name": "my-rule"},
		"spec": map[string]any{
			"listenerArn": "arn:aws:elasticloadbalancing:us-east-1:123:listener/app/alb/lb/listener",
			"priority":    10,
			"conditions":  []any{map[string]any{"field": "path-pattern", "values": []any{"/api/*"}}},
			"actions":     []any{map[string]any{"type": "forward", "targetGroupArn": "arn:tg"}},
			"tags":        map[string]any{"env": "dev"},
		},
	}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	spec, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	require.NotNil(t, spec)
}

func TestListenerRuleAdapter_ExtractRegionFromListenerArn(t *testing.T) {
	tests := []struct {
		arn    string
		region string
	}{
		{"arn:aws:elasticloadbalancing:us-east-1:123:listener/app/alb/lb/listener", "us-east-1"},
		{"arn:aws:elasticloadbalancing:eu-west-1:456:listener/app/alb/lb/listener", "eu-west-1"},
		{"invalid-arn", ""},
	}
	for _, tt := range tests {
		t.Run(tt.arn, func(t *testing.T) {
			assert.Equal(t, tt.region, extractRegionFromListenerArn(tt.arn))
		})
	}
}

func TestListenerRuleAdapter_Kind(t *testing.T) {
	adapter := NewListenerRuleAdapterWithAuth(nil)
	assert.Equal(t, "ListenerRule", adapter.Kind())
	assert.Equal(t, "ListenerRule", adapter.ServiceName())
}

func TestListenerRuleAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewListenerRuleAdapterWithAuth(nil)
	outputs := map[string]any{"ruleArn": "arn:rule", "priority": 10}
	// NormalizeOutputs expects a listenerrule.ListenerRuleOutputs struct
	// It won't work with a raw map, so we just test with a nil/error scenario
	_, err := adapter.NormalizeOutputs(outputs)
	assert.Error(t, err, "should fail with wrong type")
}
