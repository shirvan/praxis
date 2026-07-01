package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/dynamodbtable"
)

func TestDynamoDBTableAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewDynamoDBTableAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"DynamoDBTable",
		"metadata":{"name":"orders"},
		"spec":{"region":"us-east-1","hashKey":"pk","hashKeyType":"S","rangeKey":"sk","rangeKeyType":"S","billingMode":"PROVISIONED","readCapacity":5,"writeCapacity":5,"tags":{"env":"dev"}}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~orders", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(dynamodbtable.DynamoDBTableSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "orders", typed.Name)
	assert.Equal(t, "pk", typed.HashKey)
	assert.Equal(t, "S", typed.HashKeyType)
	assert.Equal(t, "sk", typed.RangeKey)
	assert.Equal(t, "PROVISIONED", typed.BillingMode)
	assert.Equal(t, int64(5), typed.ReadCapacity)
	assert.Equal(t, int64(5), typed.WriteCapacity)
	assert.Equal(t, map[string]string{"env": "dev"}, typed.Tags)
}

func TestDynamoDBTableAdapter_DecodeSpec_MissingRegion(t *testing.T) {
	adapter := NewDynamoDBTableAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"kind":"DynamoDBTable",
		"metadata":{"name":"orders"},
		"spec":{"hashKey":"pk"}
	}`)

	_, err := adapter.DecodeSpec(raw)
	assert.ErrorContains(t, err, "spec.region is required")
}

func TestDynamoDBTableAdapter_BuildImportKey(t *testing.T) {
	adapter := NewDynamoDBTableAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "orders")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~orders", key)
}

func TestDynamoDBTableAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewDynamoDBTableAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(dynamodbtable.DynamoDBTableOutputs{
		ARN:       "arn:aws:dynamodb:us-east-1:123:table/orders",
		Name:      "orders",
		Status:    "ACTIVE",
		ItemCount: 42,
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:dynamodb:us-east-1:123:table/orders", out["arn"])
	assert.Equal(t, "orders", out["name"])
	assert.Equal(t, "ACTIVE", out["status"])
	assert.Equal(t, int64(42), out["itemCount"])
}

func TestDynamoDBTableAdapter_NormalizeOutputs_OmitsEmptyOptionals(t *testing.T) {
	adapter := NewDynamoDBTableAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(dynamodbtable.DynamoDBTableOutputs{
		Name:   "orders",
		Status: "ACTIVE",
	})
	require.NoError(t, err)
	assert.NotContains(t, out, "arn")
	assert.NotContains(t, out, "itemCount")
}
