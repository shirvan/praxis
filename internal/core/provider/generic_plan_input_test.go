package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/alb"
	"github.com/shirvan/praxis/internal/drivers/dbparametergroup"
	"github.com/shirvan/praxis/internal/drivers/lambdalayer"
	"github.com/shirvan/praxis/internal/drivers/nlb"
	"github.com/shirvan/praxis/internal/drivers/sg"
	"github.com/shirvan/praxis/internal/drivers/targetgroup"
)

func TestFallbackPlanIdentities(t *testing.T) {
	tests := []struct {
		name     string
		identity func() (string, bool)
		want     string
	}{
		{
			name: "ALB desired name",
			identity: func() (string, bool) {
				return albDescriptor().PlanIdentity(alb.ALBSpec{Name: "web"}, alb.ALBOutputs{})
			},
			want: "web",
		},
		{
			name: "NLB desired name",
			identity: func() (string, bool) {
				return nlbDescriptor().PlanIdentity(nlb.NLBSpec{Name: "edge"}, nlb.NLBOutputs{})
			},
			want: "edge",
		},
		{
			name: "target group desired name",
			identity: func() (string, bool) {
				return targetGroupDescriptor().PlanIdentity(targetgroup.TargetGroupSpec{Name: "api"}, targetgroup.TargetGroupOutputs{})
			},
			want: "api",
		},
		{
			name: "parameter group desired name",
			identity: func() (string, bool) {
				return dbParameterGroupDescriptor().PlanIdentity(dbparametergroup.DBParameterGroupSpec{GroupName: "params"}, dbparametergroup.DBParameterGroupOutputs{})
			},
			want: "params",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identity, probe := test.identity()
			assert.True(t, probe)
			assert.Equal(t, test.want, identity)
		})
	}
}

func TestPlanIdentityUsesStoredProviderIdentityFirst(t *testing.T) {
	identity, probe := albDescriptor().PlanIdentity(
		alb.ALBSpec{Name: "desired-name"},
		alb.ALBOutputs{LoadBalancerArn: "arn:stored"},
	)
	require.True(t, probe)
	assert.Equal(t, "arn:stored", identity)
}

func TestLambdaLayerPlanIdentityRequiresPersistedVersion(t *testing.T) {
	descriptor := lambdaLayerDescriptor()

	identity, probe := descriptor.PlanIdentity(
		lambdalayer.LambdaLayerSpec{LayerName: "utilities"},
		lambdalayer.LambdaLayerOutputs{LayerName: "utilities"},
	)
	assert.Equal(t, "utilities", identity)
	assert.False(t, probe)

	identity, probe = descriptor.PlanIdentity(
		lambdalayer.LambdaLayerSpec{LayerName: "utilities"},
		lambdalayer.LambdaLayerOutputs{LayerName: "utilities", LayerVersionArn: "arn:version"},
	)
	assert.Equal(t, "utilities", identity)
	assert.True(t, probe)
}

func TestSecurityGroupPlanIdentityRequiresVpcAndName(t *testing.T) {
	descriptor := securityGroupDescriptor()

	_, probe := descriptor.PlanIdentity(sg.SecurityGroupSpec{GroupName: "web"}, sg.SecurityGroupOutputs{})
	assert.False(t, probe)

	identity, probe := descriptor.PlanIdentity(sg.SecurityGroupSpec{GroupName: "web", VpcId: "vpc-1"}, sg.SecurityGroupOutputs{})
	assert.True(t, probe)
	assert.Equal(t, "web", identity)
}

func TestDBParameterGroupProbeReceivesDesiredType(t *testing.T) {
	api := &capturingParameterGroupAPI{}
	probe := dbParameterGroupProbe(api)
	input := PlanProbeInput[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs]{
		Identity: "params",
		Desired:  dbparametergroup.DBParameterGroupSpec{Type: dbparametergroup.TypeCluster},
	}

	_, found, err := probe(nil, input)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "params", api.groupName)
	assert.Equal(t, dbparametergroup.TypeCluster, api.groupType)
}

type capturingParameterGroupAPI struct {
	groupName string
	groupType string
}

func (a *capturingParameterGroupAPI) CreateParameterGroup(context.Context, dbparametergroup.DBParameterGroupSpec) (string, error) {
	return "", nil
}

func (a *capturingParameterGroupAPI) DescribeParameterGroup(_ context.Context, groupName, groupType string) (dbparametergroup.ObservedState, error) {
	a.groupName = groupName
	a.groupType = groupType
	return dbparametergroup.ObservedState{}, nil
}

func (a *capturingParameterGroupAPI) UpdateParameters(context.Context, dbparametergroup.DBParameterGroupSpec, dbparametergroup.ObservedState) error {
	return nil
}

func (a *capturingParameterGroupAPI) DeleteParameterGroup(context.Context, string, string) error {
	return nil
}

func (a *capturingParameterGroupAPI) UpdateTags(context.Context, string, map[string]string) error {
	return nil
}
