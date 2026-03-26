package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/dashboard"
)

func TestDashboardAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewDashboardAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"Dashboard",
		"metadata":{"name":"ops-main"},
		"spec":{
			"region":"us-east-1",
			"dashboardBody":"{\"widgets\":[]}"
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~ops-main", key)

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(dashboard.DashboardSpec)
	require.True(t, ok)
	assert.Equal(t, "ops-main", typed.DashboardName)
	assert.Equal(t, "{\"widgets\":[]}", typed.DashboardBody)
}

func TestDashboardAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewDashboardAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(dashboard.DashboardOutputs{
		DashboardArn:  "arn:aws:cloudwatch::123456789012:dashboard/ops-main",
		DashboardName: "ops-main",
	})
	require.NoError(t, err)
	assert.Equal(t, "ops-main", out["dashboardName"])
	assert.Equal(t, "arn:aws:cloudwatch::123456789012:dashboard/ops-main", out["dashboardArn"])
}
