package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	s3drv "github.com/shirvan/praxis/internal/drivers/s3"
)

func TestS3Adapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"S3Bucket",
		"metadata":{"name":"my-bucket"},
		"spec":{
			"region":"us-east-1",
			"versioning":true,
			"acl":"private",
			"encryption":{"enabled":true,"algorithm":"AES256"},
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "my-bucket", key)
	assert.Equal(t, KeyScopeGlobal, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(s3drv.S3BucketSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "my-bucket", typed.BucketName)
	assert.True(t, typed.Versioning)
	assert.Equal(t, "private", typed.ACL)
	assert.True(t, typed.Encryption.Enabled)
	assert.Equal(t, "AES256", typed.Encryption.Algorithm)
}

func TestS3Adapter_BuildImportKey_GlobalScope(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "my-imported-bucket")
	require.NoError(t, err)
	// S3 is globally scoped, so import key is just the resource ID
	assert.Equal(t, "my-imported-bucket", key)
}

func TestS3Adapter_KindAndServiceName(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	assert.Equal(t, "S3Bucket", adapter.Kind())
	assert.Equal(t, "S3Bucket", adapter.ServiceName())
}

func TestS3Adapter_Scope(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	assert.Equal(t, KeyScopeGlobal, adapter.Scope())
}

func TestS3Adapter_ImplementsCleanupHooks(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	_, ok := any(adapter).(PreDeleter)
	assert.True(t, ok)

	timeouts, ok := any(adapter).(TimeoutDefaultsProvider)
	assert.True(t, ok)
	assert.Equal(t, "10m", timeouts.DefaultTimeouts().Delete)
}

func TestS3Adapter_NormalizeOutputs_AllFields(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(s3drv.S3BucketOutputs{
		ARN:        "arn:aws:s3:::prod-bucket",
		BucketName: "prod-bucket",
		Region:     "eu-west-1",
		DomainName: "prod-bucket.s3.eu-west-1.amazonaws.com",
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:s3:::prod-bucket", out["arn"])
	assert.Equal(t, "prod-bucket", out["bucketName"])
	assert.Equal(t, "eu-west-1", out["region"])
	assert.Equal(t, "prod-bucket.s3.eu-west-1.amazonaws.com", out["domainName"])
}

func TestS3Adapter_DecodeSpec_MissingRegion(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"S3Bucket",
		"metadata":{"name":"my-bucket"},
		"spec":{"versioning":true}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "region")
}

func TestS3Adapter_DecodeSpec_MissingName(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"S3Bucket",
		"metadata":{"name":""},
		"spec":{"region":"us-east-1"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}

func TestS3Adapter_BuildKey_MissingName(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"S3Bucket",
		"metadata":{"name":""},
		"spec":{"region":"us-east-1"}
	}`)
	_, err := adapter.BuildKey(raw)
	require.Error(t, err)
}
