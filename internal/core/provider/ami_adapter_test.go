package provider

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/ami"
)

type mockAMIAPI struct {
	describeByID map[string]ami.ObservedState
}

func (m *mockAMIAPI) RegisterImage(context.Context, ami.AMISpec) (string, error) { return "", nil }
func (m *mockAMIAPI) CopyImage(context.Context, ami.AMISpec) (string, error)     { return "", nil }
func (m *mockAMIAPI) DescribeImage(_ context.Context, imageId string) (ami.ObservedState, error) {
	return m.describeByID[imageId], nil
}
func (m *mockAMIAPI) DescribeImageByName(context.Context, string) (ami.ObservedState, error) {
	return ami.ObservedState{}, nil
}
func (m *mockAMIAPI) DeregisterImage(context.Context, string) error               { return nil }
func (m *mockAMIAPI) UpdateTags(context.Context, string, map[string]string) error { return nil }
func (m *mockAMIAPI) ModifyDescription(context.Context, string, string) error     { return nil }
func (m *mockAMIAPI) ModifyLaunchPermissions(context.Context, string, *ami.LaunchPermsSpec) error {
	return nil
}
func (m *mockAMIAPI) EnableDeprecation(context.Context, string, string) error         { return nil }
func (m *mockAMIAPI) DisableDeprecation(context.Context, string) error                { return nil }
func (m *mockAMIAPI) WaitUntilAvailable(context.Context, string, time.Duration) error { return nil }
func (m *mockAMIAPI) FindByManagedKey(context.Context, string) (string, error)        { return "", nil }

func TestAMIAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewAMIAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"AMI",
		"metadata":{"name":"web-ami"},
		"spec":{
			"region":"us-east-1",
			"description":"golden image",
			"source":{"fromAMI":{"sourceImageId":"ami-0123456789abcdef0"}},
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~web-ami", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(ami.AMISpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "web-ami", typed.Name)
	assert.Equal(t, "ami-0123456789abcdef0", typed.Source.FromAMI.SourceImageId)
	assert.Equal(t, "web-ami", typed.Tags["Name"])
}

func TestAMIAdapter_BuildImportKey_Name(t *testing.T) {
	adapter := NewAMIAdapterWithAPI(&mockAMIAPI{})
	key, err := adapter.BuildImportKey("us-east-1", "web-ami")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~web-ami", key)
}

func TestAMIAdapter_BuildImportKey_AMIID(t *testing.T) {
	adapter := NewAMIAdapterWithAPI(&mockAMIAPI{describeByID: map[string]ami.ObservedState{
		"ami-123": {Name: "resolved-name"},
	}})
	key, err := adapter.BuildImportKey("us-east-1", "ami-123")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~resolved-name", key)
}

func TestAMIAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewAMIAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(ami.AMIOutputs{
		ImageId:            "ami-123",
		Name:               "web-ami",
		State:              "available",
		Architecture:       "x86_64",
		VirtualizationType: "hvm",
		RootDeviceName:     "/dev/xvda",
		OwnerId:            "123",
		CreationDate:       "2026-01-01T00:00:00Z",
	})
	require.NoError(t, err)
	assert.Equal(t, "ami-123", out["imageId"])
	assert.Equal(t, "web-ami", out["name"])
	assert.Equal(t, "available", out["state"])
	assert.Equal(t, "x86_64", out["architecture"])
	assert.Equal(t, "hvm", out["virtualizationType"])
	assert.Equal(t, "/dev/xvda", out["rootDeviceName"])
	assert.Equal(t, "123", out["ownerId"])
	assert.Equal(t, "2026-01-01T00:00:00Z", out["creationDate"])
}

func TestAMIAdapter_KindAndScope(t *testing.T) {
	adapter := NewAMIAdapterWithAuth(nil)
	assert.Equal(t, ami.ServiceName, adapter.Kind())
	assert.Equal(t, ami.ServiceName, adapter.ServiceName())
	assert.Equal(t, KeyScopeRegion, adapter.Scope())
}
