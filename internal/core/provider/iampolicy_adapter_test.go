package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/iampolicy"
)

func TestIAMPolicyAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewIAMPolicyAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"IAMPolicy",
		"metadata":{"name":"app-policy"},
		"spec":{
			"path":"/app/",
			"policyDocument":"{\"Version\":\"2012-10-17\",\"Statement\":[]}",
			"description":"app policy",
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "app-policy", key)
	assert.Equal(t, KeyScopeGlobal, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(iampolicy.IAMPolicySpec)
	require.True(t, ok)
	assert.Equal(t, "/app/", typed.Path)
	assert.Equal(t, "app-policy", typed.PolicyName)
	assert.Equal(t, "app policy", typed.Description)
	assert.Equal(t, "dev", typed.Tags["env"])
}

func TestIAMPolicyAdapter_BuildImportKey(t *testing.T) {
	adapter := NewIAMPolicyAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "app-policy")
	require.NoError(t, err)
	assert.Equal(t, "app-policy", key)
}

func TestIAMPolicyAdapter_Kind(t *testing.T) {
	adapter := NewIAMPolicyAdapterWithAuth(nil)
	assert.Equal(t, iampolicy.ServiceName, adapter.Kind())
	assert.Equal(t, iampolicy.ServiceName, adapter.ServiceName())
}

func TestIAMPolicyAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewIAMPolicyAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(iampolicy.IAMPolicyOutputs{
		Arn:        "arn:aws:iam::123456789012:policy/app-policy",
		PolicyId:   "ANPAEXAMPLE",
		PolicyName: "app-policy",
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::123456789012:policy/app-policy", out["arn"])
	assert.Equal(t, "ANPAEXAMPLE", out["policyId"])
	assert.Equal(t, "app-policy", out["policyName"])
}
