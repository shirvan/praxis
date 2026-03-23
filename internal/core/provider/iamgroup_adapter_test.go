package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/iamgroup"
)

func TestIAMGroupAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewIAMGroupAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"IAMGroup",
		"metadata":{"name":"app-group"},
		"spec":{
			"path":"/app/",
			"inlinePolicies":{"inline-access":"{\"Version\":\"2012-10-17\",\"Statement\":[]}"},
			"managedPolicyArns":["arn:aws:iam::123456789012:policy/app-policy"]
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "app-group", key)
	assert.Equal(t, KeyScopeGlobal, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(iamgroup.IAMGroupSpec)
	require.True(t, ok)
	assert.Equal(t, "/app/", typed.Path)
	assert.Equal(t, "app-group", typed.GroupName)
	assert.Equal(t, []string{"arn:aws:iam::123456789012:policy/app-policy"}, typed.ManagedPolicyArns)
	assert.Contains(t, typed.InlinePolicies, "inline-access")
}

func TestIAMGroupAdapter_BuildImportKey(t *testing.T) {
	adapter := NewIAMGroupAdapter()
	key, err := adapter.BuildImportKey("us-east-1", "app-group")
	require.NoError(t, err)
	assert.Equal(t, "app-group", key)
}

func TestIAMGroupAdapter_Kind(t *testing.T) {
	adapter := NewIAMGroupAdapter()
	assert.Equal(t, iamgroup.ServiceName, adapter.Kind())
	assert.Equal(t, iamgroup.ServiceName, adapter.ServiceName())
}

func TestIAMGroupAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewIAMGroupAdapter()
	out, err := adapter.NormalizeOutputs(iamgroup.IAMGroupOutputs{
		Arn:       "arn:aws:iam::123456789012:group/app-group",
		GroupId:   "AGPAEXAMPLE",
		GroupName: "app-group",
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::123456789012:group/app-group", out["arn"])
	assert.Equal(t, "AGPAEXAMPLE", out["groupId"])
	assert.Equal(t, "app-group", out["groupName"])
}
