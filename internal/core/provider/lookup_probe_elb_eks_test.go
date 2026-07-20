package provider

import (
	"context"
	"errors"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	albdriver "github.com/shirvan/praxis/internal/drivers/alb"
	eksdriver "github.com/shirvan/praxis/internal/drivers/ekscluster"
	nlbdriver "github.com/shirvan/praxis/internal/drivers/nlb"
	targetgroupdriver "github.com/shirvan/praxis/internal/drivers/targetgroup"
)

type albBatchLookupAPIStub struct {
	albdriver.ALBAPI
	observed albdriver.ObservedState
	err      error
}

func (s albBatchLookupAPIStub) DescribeALB(context.Context, string) (albdriver.ObservedState, error) {
	return s.observed, s.err
}

type nlbBatchLookupAPIStub struct {
	nlbdriver.NLBAPI
	observed nlbdriver.ObservedState
	err      error
}

func (s nlbBatchLookupAPIStub) DescribeNLB(context.Context, string) (nlbdriver.ObservedState, error) {
	return s.observed, s.err
}

type targetGroupBatchLookupAPIStub struct {
	targetgroupdriver.TargetGroupAPI
	observed targetgroupdriver.ObservedState
	err      error
}

func (s targetGroupBatchLookupAPIStub) DescribeTargetGroup(context.Context, string) (targetgroupdriver.ObservedState, error) {
	return s.observed, s.err
}

type eksBatchLookupAPIStub struct {
	eksdriver.EKSClusterAPI
	observed eksdriver.ObservedState
	found    bool
	err      error
}

func (s eksBatchLookupAPIStub) DescribeCluster(context.Context, string) (eksdriver.ObservedState, bool, error) {
	return s.observed, s.found, s.err
}

func TestALBLookupProbe_IDNameAndTagsAreANDConstraints(t *testing.T) {
	arn := "arn:aws:elasticloadbalancing:us-west-2:123:loadbalancer/app/payments/abc"
	probe := albLookupProbe(albBatchLookupAPIStub{observed: albdriver.ObservedState{
		LoadBalancerArn: arn,
		Name:            "payments",
		DnsName:         "payments.example",
		HostedZoneId:    "Z123",
		VpcId:           "vpc-123",
		Tags:            map[string]string{"environment": "prod"},
	}})

	outputs, found, err := probe(nil, LookupFilter{
		ID: arn, Name: "payments", Tag: map[string]string{"environment": "prod"},
	})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, arn, outputs.LoadBalancerArn)
	assert.Equal(t, "payments.example", outputs.DnsName)
	assert.Equal(t, "Z123", outputs.CanonicalHostedZoneId)
}

func TestNLBLookupProbe_RejectsMismatchedTag(t *testing.T) {
	probe := nlbLookupProbe(nlbBatchLookupAPIStub{observed: nlbdriver.ObservedState{
		LoadBalancerArn: "arn:aws:elasticloadbalancing:us-west-2:123:loadbalancer/net/payments/abc",
		Name:            "payments",
		Tags:            map[string]string{"environment": "dev"},
	}})

	_, found, err := probe(nil, LookupFilter{Name: "payments", Tag: map[string]string{"environment": "prod"}})
	require.NoError(t, err)
	assert.False(t, found)
}

func TestTargetGroupLookupProbe_ByName(t *testing.T) {
	probe := targetGroupLookupProbe(targetGroupBatchLookupAPIStub{observed: targetgroupdriver.ObservedState{
		TargetGroupArn: "arn:aws:elasticloadbalancing:us-west-2:123:targetgroup/payments/abc",
		Name:           "payments",
		Tags:           map[string]string{"environment": "prod"},
	}})

	outputs, found, err := probe(nil, LookupFilter{Name: "payments", Tag: map[string]string{"environment": "prod"}})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "payments", outputs.TargetGroupName)
}

func TestEKSClusterLookupProbe_ByNativeIdentifier(t *testing.T) {
	probe := eksClusterLookupProbe(eksBatchLookupAPIStub{found: true, observed: eksdriver.ObservedState{
		ARN: "arn:aws:eks:us-west-2:123:cluster/payments", Name: "payments", Status: "ACTIVE",
		Version: "1.33", PlatformVersion: "eks.1", Endpoint: "https://example.eks",
		Tags: map[string]string{"environment": "prod"},
	}})

	outputs, found, err := probe(nil, LookupFilter{ID: "payments", Tag: map[string]string{"environment": "prod"}})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "ACTIVE", outputs.Status)
	assert.Equal(t, "https://example.eks", outputs.Endpoint)
}

func TestLookupProbeBatch_TagOnlyIsTerminalValidation(t *testing.T) {
	assertTerminal400 := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		assert.True(t, restate.IsTerminalError(err))
		assert.Equal(t, uint16(400), uint16(restate.ErrorCode(err)))
	}

	t.Run("ALB", func(t *testing.T) {
		_, _, err := albLookupProbe(albBatchLookupAPIStub{})(nil, LookupFilter{Tag: map[string]string{"environment": "prod"}})
		assertTerminal400(t, err)
	})
	t.Run("NLB", func(t *testing.T) {
		_, _, err := nlbLookupProbe(nlbBatchLookupAPIStub{})(nil, LookupFilter{Tag: map[string]string{"environment": "prod"}})
		assertTerminal400(t, err)
	})
	t.Run("TargetGroup", func(t *testing.T) {
		_, _, err := targetGroupLookupProbe(targetGroupBatchLookupAPIStub{})(nil, LookupFilter{Tag: map[string]string{"environment": "prod"}})
		assertTerminal400(t, err)
	})
	t.Run("EKSCluster", func(t *testing.T) {
		_, _, err := eksClusterLookupProbe(eksBatchLookupAPIStub{})(nil, LookupFilter{Tag: map[string]string{"environment": "prod"}})
		assertTerminal400(t, err)
	})
}

func TestLookupProbeBatch_NotFoundIsAbsent(t *testing.T) {
	probe := albLookupProbe(albBatchLookupAPIStub{err: errors.New("ALB not found")})
	_, found, err := probe(nil, LookupFilter{Name: "missing"})
	require.NoError(t, err)
	assert.False(t, found)
}

func TestLookupProbeBatch_ProviderErrorRemainsRetryable(t *testing.T) {
	want := errors.New("temporary provider failure")
	probe := nlbLookupProbe(nlbBatchLookupAPIStub{err: want})
	_, _, err := probe(nil, LookupFilter{Name: "payments"})
	assert.ErrorIs(t, err, want)
	assert.False(t, restate.IsTerminalError(err))
}
