package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/iamuser"
)

func TestIAMUserAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewIAMUserAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"IAMUser",
		"metadata":{"name":"app-user"},
		"spec":{
			"path":"/app/",
			"permissionsBoundary":"arn:aws:iam::123456789012:policy/boundary",
			"inlinePolicies":{"inline":"{\"Version\":\"2012-10-17\",\"Statement\":[]}"},
			"managedPolicyArns":["arn:aws:iam::123456789012:policy/app"],
			"groups":["devs"],
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "app-user", key)
	assert.Equal(t, KeyScopeGlobal, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(iamuser.IAMUserSpec)
	require.True(t, ok)
	assert.Equal(t, "/app/", typed.Path)
	assert.Equal(t, "app-user", typed.UserName)
	assert.Equal(t, "arn:aws:iam::123456789012:policy/boundary", typed.PermissionsBoundary)
	assert.Equal(t, []string{"devs"}, typed.Groups)
	assert.Equal(t, "dev", typed.Tags["env"])
}

func TestIAMUserAdapter_BuildImportKey(t *testing.T) {
	adapter := NewIAMUserAdapter()
	key, err := adapter.BuildImportKey("us-east-1", "app-user")
	require.NoError(t, err)
	assert.Equal(t, "app-user", key)
}

func TestIAMUserAdapter_Kind(t *testing.T) {
	adapter := NewIAMUserAdapter()
	assert.Equal(t, iamuser.ServiceName, adapter.Kind())
	assert.Equal(t, iamuser.ServiceName, adapter.ServiceName())
}

func TestIAMUserAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewIAMUserAdapter()
	out, err := adapter.NormalizeOutputs(iamuser.IAMUserOutputs{
		Arn:      "arn:aws:iam::123456789012:user/app-user",
		UserId:   "AIDAEXAMPLE",
		UserName: "app-user",
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::123456789012:user/app-user", out["arn"])
	assert.Equal(t, "AIDAEXAMPLE", out["userId"])
	assert.Equal(t, "app-user", out["userName"])
}
