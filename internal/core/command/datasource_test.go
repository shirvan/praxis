package command

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/internal/core/template"
	"github.com/shirvan/praxis/pkg/types"
)

func TestValidateDataSources_NameCollision(t *testing.T) {
	service := &PraxisCommandService{providers: provider.NewRegistryWithAdapters(provider.NewVPCAdapterWithAuth(nil))}
	err := service.validateDataSources(map[string]template.DataSourceSpec{
		"vpc": {Kind: "VPC", Filter: template.DataSourceFilter{Name: "prod"}},
	}, map[string]bool{"vpc": true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflicts with resource")
}

func TestValidateDataSources_EmptyFilter(t *testing.T) {
	service := &PraxisCommandService{providers: provider.NewRegistryWithAdapters(provider.NewVPCAdapterWithAuth(nil))}
	err := service.validateDataSources(map[string]template.DataSourceSpec{
		"vpc": {Kind: "VPC"},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "filter must specify")
}

func TestSubstituteDataExprs_ReplacesTypedValues(t *testing.T) {
	specs := map[string]json.RawMessage{
		"instance": json.RawMessage(`{"spec":{"vpcId":"${data.vpc.outputs.vpcId}","public":true,"subnets":["${data.subnet.outputs.subnetId}"],"keep":"${resources.sg.outputs.groupId}"}}`),
	}

	updated, err := substituteDataExprs(specs, map[string]types.DataSourceResult{
		"vpc":    {Kind: "VPC", Outputs: map[string]any{"vpcId": "vpc-123"}},
		"subnet": {Kind: "Subnet", Outputs: map[string]any{"subnetId": "subnet-123"}},
	})
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(updated["instance"], &decoded))
	spec := decoded["spec"].(map[string]any)
	assert.Equal(t, "vpc-123", spec["vpcId"])
	assert.Equal(t, "${resources.sg.outputs.groupId}", spec["keep"])
	assert.Equal(t, "subnet-123", spec["subnets"].([]any)[0])
}

func TestSubstituteDataExprs_MissingOutput(t *testing.T) {
	_, err := substituteDataExprs(map[string]json.RawMessage{
		"instance": json.RawMessage(`{"spec":{"vpcId":"${data.vpc.outputs.vpcId}"}}`),
	}, map[string]types.DataSourceResult{
		"vpc": {Kind: "VPC", Outputs: map[string]any{"cidrBlock": "10.0.0.0/16"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "available")
}
