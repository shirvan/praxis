package provider

import (
	"context"
	"errors"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	amidriver "github.com/shirvan/praxis/internal/drivers/ami"
	dashboarddriver "github.com/shirvan/praxis/internal/drivers/dashboard"
	ecrpolicydriver "github.com/shirvan/praxis/internal/drivers/ecrpolicy"
	layerdriver "github.com/shirvan/praxis/internal/drivers/lambdalayer"
	alarmdriver "github.com/shirvan/praxis/internal/drivers/metricalarm"
)

type amiNativeLookupAPIStub struct {
	amidriver.AMIAPI
	observed amidriver.ObservedState
	err      error
}

func (s amiNativeLookupAPIStub) DescribeImage(context.Context, string) (amidriver.ObservedState, error) {
	return s.observed, s.err
}

func (s amiNativeLookupAPIStub) DescribeImageByName(context.Context, string) (amidriver.ObservedState, error) {
	return s.observed, s.err
}

type ecrPolicyNativeLookupAPIStub struct {
	ecrpolicydriver.LifecyclePolicyAPI
	observed ecrpolicydriver.ObservedState
	err      error
}

func (s ecrPolicyNativeLookupAPIStub) GetLifecyclePolicy(context.Context, string) (ecrpolicydriver.ObservedState, error) {
	return s.observed, s.err
}

type layerNativeLookupAPIStub struct {
	layerdriver.LayerAPI
	observed layerdriver.ObservedState
	err      error
}

func (s layerNativeLookupAPIStub) GetLatestLayerVersion(context.Context, string) (layerdriver.ObservedState, error) {
	return s.observed, s.err
}

type dashboardNativeLookupAPIStub struct {
	dashboarddriver.DashboardAPI
	observed dashboarddriver.ObservedState
	found    bool
	err      error
}

func (s dashboardNativeLookupAPIStub) GetDashboard(context.Context, string) (dashboarddriver.ObservedState, bool, error) {
	return s.observed, s.found, s.err
}

type alarmNativeLookupAPIStub struct {
	alarmdriver.MetricAlarmAPI
	observed alarmdriver.ObservedState
	found    bool
	err      error
}

func (s alarmNativeLookupAPIStub) DescribeAlarm(context.Context, string) (alarmdriver.ObservedState, bool, error) {
	return s.observed, s.found, s.err
}

func TestAMILookupProbe_IDNameAndTagsAreANDConstraints(t *testing.T) {
	probe := amiLookupProbe(amiNativeLookupAPIStub{observed: amidriver.ObservedState{
		ImageId: "ami-123", Name: "payments", State: "available", Architecture: "arm64",
		VirtualizationType: "hvm", RootDeviceName: "/dev/xvda", OwnerId: "123",
		CreationDate: "2026-07-19T00:00:00Z", Tags: map[string]string{"environment": "prod"},
	}})

	outputs, found, err := probe(nil, LookupFilter{
		ID: "ami-123", Name: "payments", Tag: map[string]string{"environment": "prod"},
	})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "ami-123", outputs.ImageId)
	assert.Equal(t, "arm64", outputs.Architecture)
	assert.Equal(t, "/dev/xvda", outputs.RootDeviceName)
}

func TestECRLifecyclePolicyLookupProbe_ByRepositoryName(t *testing.T) {
	probe := ecrLifecyclePolicyLookupProbe(ecrPolicyNativeLookupAPIStub{observed: ecrpolicydriver.ObservedState{
		RepositoryName: "payments", RepositoryArn: "arn:aws:ecr:us-west-2:123:repository/payments", RegistryId: "123",
	}})

	outputs, found, err := probe(nil, LookupFilter{Name: "payments"})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "payments", outputs.RepositoryName)
	assert.Equal(t, "123", outputs.RegistryId)
}

func TestLambdaLayerLookupProbe_ByName(t *testing.T) {
	probe := lambdaLayerLookupProbe(layerNativeLookupAPIStub{observed: layerdriver.ObservedState{
		LayerArn: "arn:aws:lambda:us-west-2:123:layer:shared", LayerVersionArn: "arn:aws:lambda:us-west-2:123:layer:shared:7",
		LayerName: "shared", Version: 7, CodeSize: 2048, CodeSha256: "sha", CreatedDate: "2026-07-19",
	}})

	outputs, found, err := probe(nil, LookupFilter{Name: "shared"})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(7), outputs.Version)
	assert.Equal(t, int64(2048), outputs.CodeSize)
}

func TestDashboardLookupProbe_ByName(t *testing.T) {
	probe := dashboardLookupProbe(dashboardNativeLookupAPIStub{found: true, observed: dashboarddriver.ObservedState{
		DashboardArn: "arn:aws:cloudwatch::123:dashboard/payments", DashboardName: "payments",
	}})

	outputs, found, err := probe(nil, LookupFilter{Name: "payments"})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "arn:aws:cloudwatch::123:dashboard/payments", outputs.DashboardArn)
}

func TestMetricAlarmLookupProbe_ByNameAndTags(t *testing.T) {
	probe := metricAlarmLookupProbe(alarmNativeLookupAPIStub{found: true, observed: alarmdriver.ObservedState{
		AlarmArn: "arn:aws:cloudwatch:us-west-2:123:alarm:high-errors", AlarmName: "high-errors",
		StateValue: "OK", StateReason: "threshold not crossed", Tags: map[string]string{"environment": "prod"},
	}})

	outputs, found, err := probe(nil, LookupFilter{Name: "high-errors", Tag: map[string]string{"environment": "prod"}})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "OK", outputs.StateValue)
	assert.Equal(t, "threshold not crossed", outputs.StateReason)
}

func TestNativeReadLookupBatch_RejectsUnsupportedSelectors(t *testing.T) {
	assertTerminal400 := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		assert.True(t, restate.IsTerminalError(err))
		assert.Equal(t, uint16(400), uint16(restate.ErrorCode(err)))
	}

	t.Run("AMI tag-only", func(t *testing.T) {
		_, _, err := amiLookupProbe(amiNativeLookupAPIStub{})(nil, LookupFilter{Tag: map[string]string{"environment": "prod"}})
		assertTerminal400(t, err)
	})
	t.Run("ECR policy tags", func(t *testing.T) {
		_, _, err := ecrLifecyclePolicyLookupProbe(ecrPolicyNativeLookupAPIStub{})(nil, LookupFilter{Name: "payments", Tag: map[string]string{"environment": "prod"}})
		assertTerminal400(t, err)
	})
	t.Run("Lambda layer id", func(t *testing.T) {
		_, _, err := lambdaLayerLookupProbe(layerNativeLookupAPIStub{})(nil, LookupFilter{ID: "arn:aws:lambda:us-west-2:123:layer:shared:7"})
		assertTerminal400(t, err)
	})
	t.Run("Lambda layer tags", func(t *testing.T) {
		_, _, err := lambdaLayerLookupProbe(layerNativeLookupAPIStub{})(nil, LookupFilter{Name: "shared", Tag: map[string]string{"environment": "prod"}})
		assertTerminal400(t, err)
	})
	t.Run("Dashboard id", func(t *testing.T) {
		_, _, err := dashboardLookupProbe(dashboardNativeLookupAPIStub{})(nil, LookupFilter{ID: "arn:aws:cloudwatch::123:dashboard/payments"})
		assertTerminal400(t, err)
	})
	t.Run("Dashboard tags", func(t *testing.T) {
		_, _, err := dashboardLookupProbe(dashboardNativeLookupAPIStub{})(nil, LookupFilter{Name: "payments", Tag: map[string]string{"environment": "prod"}})
		assertTerminal400(t, err)
	})
	t.Run("Metric alarm id", func(t *testing.T) {
		_, _, err := metricAlarmLookupProbe(alarmNativeLookupAPIStub{})(nil, LookupFilter{ID: "arn:aws:cloudwatch:us-west-2:123:alarm:high-errors"})
		assertTerminal400(t, err)
	})
	t.Run("Metric alarm tag-only", func(t *testing.T) {
		_, _, err := metricAlarmLookupProbe(alarmNativeLookupAPIStub{})(nil, LookupFilter{Tag: map[string]string{"environment": "prod"}})
		assertTerminal400(t, err)
	})
}

func TestNativeReadLookupBatch_NotFoundIsAbsent(t *testing.T) {
	probe := lambdaLayerLookupProbe(layerNativeLookupAPIStub{err: errors.New("layer not found")})
	_, found, err := probe(nil, LookupFilter{Name: "missing"})
	require.NoError(t, err)
	assert.False(t, found)
}

func TestNativeReadLookupBatch_ProviderErrorRemainsRetryable(t *testing.T) {
	want := errors.New("temporary provider failure")
	probe := dashboardLookupProbe(dashboardNativeLookupAPIStub{err: want})
	_, _, err := probe(nil, LookupFilter{Name: "payments"})
	assert.ErrorIs(t, err, want)
	assert.False(t, restate.IsTerminalError(err))
}
