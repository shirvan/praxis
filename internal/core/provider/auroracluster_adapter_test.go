package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/auroracluster"
)

func TestAuroraClusterAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewAuroraClusterAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"AuroraCluster",
		"metadata":{"name":"my-cluster"},
		"spec":{
			"region":"us-east-1",
			"engine":"aurora-mysql",
			"engineVersion":"8.0.mysql_aurora.3.04.0",
			"masterUsername":"admin",
			"masterUserPassword":"secret123",
			"storageEncrypted":true,
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~my-cluster", key)

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(auroracluster.AuroraClusterSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "my-cluster", typed.ClusterIdentifier)
	assert.Equal(t, "aurora-mysql", typed.Engine)
	assert.Equal(t, "8.0.mysql_aurora.3.04.0", typed.EngineVersion)
	assert.Equal(t, "admin", typed.MasterUsername)
	assert.True(t, typed.StorageEncrypted)
}

func TestAuroraClusterAdapter_BuildImportKey(t *testing.T) {
	adapter := NewAuroraClusterAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-west-2", "my-cluster")
	require.NoError(t, err)
	assert.Equal(t, "us-west-2~my-cluster", key)
}

func TestAuroraClusterAdapter_Kind(t *testing.T) {
	adapter := NewAuroraClusterAdapterWithAuth(nil)
	assert.Equal(t, auroracluster.ServiceName, adapter.Kind())
	assert.Equal(t, auroracluster.ServiceName, adapter.ServiceName())
}

func TestAuroraClusterAdapter_Scope(t *testing.T) {
	adapter := NewAuroraClusterAdapterWithAuth(nil)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())
}

func TestAuroraClusterAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewAuroraClusterAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(auroracluster.AuroraClusterOutputs{
		ClusterIdentifier: "my-cluster",
		ClusterResourceId: "cluster-abc",
		ARN:               "arn:aws:rds:us-east-1:123:cluster:my-cluster",
		Endpoint:          "my-cluster.abc.us-east-1.rds.amazonaws.com",
		ReaderEndpoint:    "my-cluster-ro.abc.us-east-1.rds.amazonaws.com",
		Port:              3306,
		Engine:            "aurora-mysql",
		EngineVersion:     "8.0",
		Status:            "available",
	})
	require.NoError(t, err)
	assert.Equal(t, "my-cluster", out["clusterIdentifier"])
	assert.Equal(t, "cluster-abc", out["clusterResourceId"])
	assert.Equal(t, "arn:aws:rds:us-east-1:123:cluster:my-cluster", out["arn"])
	assert.Equal(t, "my-cluster.abc.us-east-1.rds.amazonaws.com", out["endpoint"])
	assert.Equal(t, "my-cluster-ro.abc.us-east-1.rds.amazonaws.com", out["readerEndpoint"])
	assert.Equal(t, int32(3306), out["port"])
	assert.Equal(t, "aurora-mysql", out["engine"])
	assert.Equal(t, "8.0", out["engineVersion"])
	assert.Equal(t, "available", out["status"])
}
