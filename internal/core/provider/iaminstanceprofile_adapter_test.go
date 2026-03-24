package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/iaminstanceprofile"
)

func TestIAMInstanceProfileAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewIAMInstanceProfileAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"IAMInstanceProfile",
		"metadata":{"name":"app-profile"},
		"spec":{
			"path":"/app/",
			"roleName":"app-role",
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "app-profile", key)
	assert.Equal(t, KeyScopeGlobal, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(iaminstanceprofile.IAMInstanceProfileSpec)
	require.True(t, ok)
	assert.Equal(t, "/app/", typed.Path)
	assert.Equal(t, "app-profile", typed.InstanceProfileName)
	assert.Equal(t, "app-role", typed.RoleName)
	assert.Equal(t, "dev", typed.Tags["env"])
}

func TestIAMInstanceProfileAdapter_BuildImportKey(t *testing.T) {
	adapter := NewIAMInstanceProfileAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "app-profile")
	require.NoError(t, err)
	assert.Equal(t, "app-profile", key)
}

func TestIAMInstanceProfileAdapter_Kind(t *testing.T) {
	adapter := NewIAMInstanceProfileAdapterWithAuth(nil)
	assert.Equal(t, iaminstanceprofile.ServiceName, adapter.Kind())
	assert.Equal(t, iaminstanceprofile.ServiceName, adapter.ServiceName())
}

func TestIAMInstanceProfileAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewIAMInstanceProfileAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(iaminstanceprofile.IAMInstanceProfileOutputs{
		Arn:                 "arn:aws:iam::123456789012:instance-profile/app-profile",
		InstanceProfileId:   "AIPAJFEXAMPLE",
		InstanceProfileName: "app-profile",
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::123456789012:instance-profile/app-profile", out["arn"])
	assert.Equal(t, "AIPAJFEXAMPLE", out["instanceProfileId"])
	assert.Equal(t, "app-profile", out["instanceProfileName"])
}
