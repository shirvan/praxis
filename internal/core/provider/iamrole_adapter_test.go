package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/iamrole"
)

func TestIAMRoleAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewIAMRoleAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"IAMRole",
		"metadata":{"name":"app-role"},
		"spec":{
			"path":"/app/",
			"assumeRolePolicyDocument":"{\"Version\":\"2012-10-17\",\"Statement\":[]}",
			"description":"app role",
			"maxSessionDuration":7200,
			"permissionsBoundary":"arn:aws:iam::123456789012:policy/boundary",
			"inlinePolicies":{"inline":"{\"Version\":\"2012-10-17\",\"Statement\":[]}"},
			"managedPolicyArns":["arn:aws:iam::123456789012:policy/app"],
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "app-role", key)
	assert.Equal(t, KeyScopeGlobal, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(iamrole.IAMRoleSpec)
	require.True(t, ok)
	assert.Equal(t, "/app/", typed.Path)
	assert.Equal(t, "app-role", typed.RoleName)
	assert.Equal(t, "app role", typed.Description)
	assert.Equal(t, int32(7200), typed.MaxSessionDuration)
	assert.Equal(t, "arn:aws:iam::123456789012:policy/boundary", typed.PermissionsBoundary)
	assert.Equal(t, "dev", typed.Tags["env"])
}

func TestIAMRoleAdapter_BuildImportKey(t *testing.T) {
	adapter := NewIAMRoleAdapter()
	key, err := adapter.BuildImportKey("us-east-1", "app-role")
	require.NoError(t, err)
	assert.Equal(t, "app-role", key)
}

func TestIAMRoleAdapter_Kind(t *testing.T) {
	adapter := NewIAMRoleAdapter()
	assert.Equal(t, iamrole.ServiceName, adapter.Kind())
	assert.Equal(t, iamrole.ServiceName, adapter.ServiceName())
}

func TestIAMRoleAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewIAMRoleAdapter()
	out, err := adapter.NormalizeOutputs(iamrole.IAMRoleOutputs{
		Arn:      "arn:aws:iam::123456789012:role/app-role",
		RoleId:   "AROAEXAMPLE",
		RoleName: "app-role",
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::123456789012:role/app-role", out["arn"])
	assert.Equal(t, "AROAEXAMPLE", out["roleId"])
	assert.Equal(t, "app-role", out["roleName"])
}
