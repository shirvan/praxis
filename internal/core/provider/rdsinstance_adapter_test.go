package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/internal/drivers/rdsinstance"
)

func TestRDSInstanceAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewRDSInstanceAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"RDSInstance",
		"metadata":{"name":"mydb"},
		"spec":{
			"region":"us-east-1",
			"engine":"mysql",
			"engineVersion":"8.0",
			"instanceClass":"db.t3.micro",
			"allocatedStorage":20,
			"storageType":"gp3",
			"masterUsername":"admin",
			"masterUserPassword":"secret123",
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~mydb", key)

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(rdsinstance.RDSInstanceSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "mydb", typed.DBIdentifier)
	assert.Equal(t, "mysql", typed.Engine)
	assert.Equal(t, "8.0", typed.EngineVersion)
	assert.Equal(t, "db.t3.micro", typed.InstanceClass)
	assert.Equal(t, int32(20), typed.AllocatedStorage)
	assert.Equal(t, "gp3", typed.StorageType)
	assert.Equal(t, "admin", typed.MasterUsername)
}

func TestRDSInstanceAdapter_BuildImportKey(t *testing.T) {
	adapter := NewRDSInstanceAdapter()
	key, err := adapter.BuildImportKey("us-west-2", "mydb")
	require.NoError(t, err)
	assert.Equal(t, "us-west-2~mydb", key)
}

func TestRDSInstanceAdapter_Kind(t *testing.T) {
	adapter := NewRDSInstanceAdapter()
	assert.Equal(t, rdsinstance.ServiceName, adapter.Kind())
	assert.Equal(t, rdsinstance.ServiceName, adapter.ServiceName())
}

func TestRDSInstanceAdapter_Scope(t *testing.T) {
	adapter := NewRDSInstanceAdapter()
	assert.Equal(t, KeyScopeRegion, adapter.Scope())
}

func TestRDSInstanceAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewRDSInstanceAdapter()
	out, err := adapter.NormalizeOutputs(rdsinstance.RDSInstanceOutputs{
		DBIdentifier:  "mydb",
		DbiResourceId: "db-abc123",
		ARN:           "arn:aws:rds:us-east-1:123:db:mydb",
		Endpoint:      "mydb.abc.us-east-1.rds.amazonaws.com",
		Port:          3306,
		Engine:        "mysql",
		EngineVersion: "8.0",
		Status:        "available",
	})
	require.NoError(t, err)
	assert.Equal(t, "mydb", out["dbIdentifier"])
	assert.Equal(t, "db-abc123", out["dbiResourceId"])
	assert.Equal(t, "arn:aws:rds:us-east-1:123:db:mydb", out["arn"])
	assert.Equal(t, "mydb.abc.us-east-1.rds.amazonaws.com", out["endpoint"])
	assert.Equal(t, int32(3306), out["port"])
	assert.Equal(t, "mysql", out["engine"])
	assert.Equal(t, "8.0", out["engineVersion"])
	assert.Equal(t, "available", out["status"])
}
