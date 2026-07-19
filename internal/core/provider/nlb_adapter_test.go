package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/nlb"
)

func TestNLBAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewNLBAdapterWithAuth(nil)
	doc := json.RawMessage(`{
		"apiVersion": "praxis.io/alpha",
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

func TestNLBAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewNLBAdapterWithAuth(nil)
	outputs, err := adapter.NormalizeOutputs(nlb.NLBOutputs{
		LoadBalancerArn: "arn:nlb", DnsName: "nlb.example.com", HostedZoneId: "zone-1",
		VpcId: "vpc-1", CanonicalHostedZoneId: "canonical-zone-1",
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"loadBalancerArn": "arn:nlb", "dnsName": "nlb.example.com", "hostedZoneId": "zone-1",
		"vpcId": "vpc-1", "canonicalHostedZoneId": "canonical-zone-1",
	}, outputs)
}
