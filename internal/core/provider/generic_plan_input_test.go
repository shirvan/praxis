package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/smithy-go"
	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/alb"
	"github.com/shirvan/praxis/internal/drivers/dbparametergroup"
	"github.com/shirvan/praxis/internal/drivers/lambdalayer"
	"github.com/shirvan/praxis/internal/drivers/nlb"
	"github.com/shirvan/praxis/internal/drivers/sg"
	"github.com/shirvan/praxis/internal/drivers/targetgroup"
)

type probeErrorCall func(error) error

func migratedProbeErrorCalls() map[string]probeErrorCall {
	return map[string]probeErrorCall{
		"ALB": func(probeErr error) error {
			_, _, err := albProbe(&errorALBAPI{err: probeErr})(nil, PlanProbeInput[alb.ALBSpec, alb.ALBOutputs]{Identity: "web"})
			return err
		},
		"NLB": func(probeErr error) error {
			_, _, err := nlbProbe(&errorNLBAPI{err: probeErr})(nil, PlanProbeInput[nlb.NLBSpec, nlb.NLBOutputs]{Identity: "edge"})
			return err
		},
		"TargetGroup": func(probeErr error) error {
			_, _, err := targetGroupProbe(&errorTargetGroupAPI{err: probeErr})(nil, PlanProbeInput[targetgroup.TargetGroupSpec, targetgroup.TargetGroupOutputs]{Identity: "api"})
			return err
		},
		"DBParameterGroup": func(probeErr error) error {
			_, _, err := dbParameterGroupProbe(&errorParameterGroupAPI{err: probeErr})(nil, PlanProbeInput[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs]{Identity: "params"})
			return err
		},
		"LambdaLayer": func(probeErr error) error {
			_, _, err := lambdaLayerProbe(&errorLayerAPI{err: probeErr})(nil, PlanProbeInput[lambdalayer.LambdaLayerSpec, lambdalayer.LambdaLayerOutputs]{Identity: "utilities"})
			return err
		},
		"SecurityGroup": func(probeErr error) error {
			_, _, err := securityGroupProbe(&errorSecurityGroupAPI{err: probeErr})(nil, PlanProbeInput[sg.SecurityGroupSpec, sg.SecurityGroupOutputs]{Identity: "web", Desired: sg.SecurityGroupSpec{VpcId: "vpc-1"}})
			return err
		},
	}
}

func TestMigratedPlanProbesPreserveRetryableErrors(t *testing.T) {
	for name, call := range migratedProbeErrorCalls() {
		t.Run(name, func(t *testing.T) {
			transportErr := errors.New("connection reset by peer")
			got := call(transportErr)
			assert.ErrorIs(t, got, transportErr)
			assert.False(t, restate.IsTerminalError(got))
		})
	}
}

func TestMigratedPlanProbeErrorsAreClassifiedAtGenericBoundary(t *testing.T) {
	boundaryCases := []struct {
		name   string
		code   string
		status uint16
	}{
		{name: "access denied", code: "AccessDeniedException", status: 403},
		{name: "validation", code: "ValidationException", status: 400},
	}
	for probeName, call := range migratedProbeErrorCalls() {
		for _, boundaryCase := range boundaryCases {
			t.Run(probeName+"/"+boundaryCase.name, func(t *testing.T) {
				providerErr := &smithy.GenericAPIError{Code: boundaryCase.code, Message: "test"}
				raw := call(providerErr)
				require.False(t, restate.IsTerminalError(raw), "the resource probe must not own shared classification")

				classified := classifyPlanProbeError(raw)
				require.True(t, restate.IsTerminalError(classified))
				assert.Equal(t, boundaryCase.status, uint16(restate.ErrorCode(classified)))
			})
		}
	}
}

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

type errorALBAPI struct {
	alb.ALBAPI
	err error
}

func (a *errorALBAPI) DescribeALB(context.Context, string) (alb.ObservedState, error) {
	return alb.ObservedState{}, a.err
}

type errorNLBAPI struct {
	nlb.NLBAPI
	err error
}

func (a *errorNLBAPI) DescribeNLB(context.Context, string) (nlb.ObservedState, error) {
	return nlb.ObservedState{}, a.err
}

type errorTargetGroupAPI struct {
	targetgroup.TargetGroupAPI
	err error
}

func (a *errorTargetGroupAPI) DescribeTargetGroup(context.Context, string) (targetgroup.ObservedState, error) {
	return targetgroup.ObservedState{}, a.err
}

type errorParameterGroupAPI struct {
	dbparametergroup.DBParameterGroupAPI
	err error
}

func (a *errorParameterGroupAPI) DescribeParameterGroup(context.Context, string, string) (dbparametergroup.ObservedState, error) {
	return dbparametergroup.ObservedState{}, a.err
}

type errorLayerAPI struct {
	lambdalayer.LayerAPI
	err error
}

func (a *errorLayerAPI) GetLatestLayerVersion(context.Context, string) (lambdalayer.ObservedState, error) {
	return lambdalayer.ObservedState{}, a.err
}

type errorSecurityGroupAPI struct {
	sg.SGAPI
	err error
}

func (a *errorSecurityGroupAPI) FindSecurityGroup(context.Context, string, string) (sg.ObservedState, error) {
	return sg.ObservedState{}, a.err
}
