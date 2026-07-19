package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/ssmparameter"
)

func TestSSMParameterAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewSSMParameterAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/alpha",
		"kind":"SSMParameter",
		"metadata":{"name":"/praxis/dev/database/password"},
		"spec":{"region":"us-east-1","type":"SecureString","value":"secret"}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~/praxis/dev/database/password", key)

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(ssmparameter.SSMParameterSpec)
	require.True(t, ok)
	assert.Equal(t, "/praxis/dev/database/password", typed.ParameterName)
	assert.Equal(t, "SecureString", typed.Type)
	assert.NotNil(t, typed.Tags)
}

func TestSSMParameterAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewSSMParameterAdapterWithAuth(nil)
	outputs, err := adapter.NormalizeOutputs(ssmparameter.SSMParameterOutputs{
		ARN:           "arn:aws:ssm:us-east-1:123456789012:parameter/praxis/dev/database/password",
		ParameterName: "/praxis/dev/database/password", Type: "SecureString", Version: 3,
		Tier: "Standard", DataType: "text",
	})
	require.NoError(t, err)
	assert.Equal(t, "/praxis/dev/database/password", outputs["parameterName"])
	assert.Equal(t, int64(3), outputs["version"])
	assert.Equal(t, "arn:aws:ssm:us-east-1:123456789012:parameter/praxis/dev/database/password", outputs["arn"])
}
