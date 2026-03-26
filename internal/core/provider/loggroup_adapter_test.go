package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/loggroup"
)

func TestLogGroupAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewLogGroupAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"LogGroup",
		"metadata":{"name":"/aws/lambda/app"},
		"spec":{
			"region":"us-east-1",
			"logGroupClass":"STANDARD",
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~/aws/lambda/app", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(loggroup.LogGroupSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "/aws/lambda/app", typed.LogGroupName)
	assert.Equal(t, "dev", typed.Tags["env"])
}

func TestLogGroupAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewLogGroupAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(loggroup.LogGroupOutputs{
		ARN:             "arn:aws:logs:us-east-1:123456789012:log-group:/aws/lambda/app",
		LogGroupName:    "/aws/lambda/app",
		LogGroupClass:   "STANDARD",
		RetentionInDays: 14,
		CreationTime:    123,
		StoredBytes:     456,
	})
	require.NoError(t, err)
	assert.Equal(t, "/aws/lambda/app", out["logGroupName"])
	assert.Equal(t, "STANDARD", out["logGroupClass"])
	assert.Equal(t, int32(14), out["retentionInDays"])
	assert.Equal(t, int64(123), out["creationTime"])
	assert.Equal(t, int64(456), out["storedBytes"])
}
