package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ecrdriver "github.com/shirvan/praxis/internal/drivers/ecrrepo"
	ecsdriver "github.com/shirvan/praxis/internal/drivers/ecscluster"
	logdriver "github.com/shirvan/praxis/internal/drivers/loggroup"
)

type ecsLookupAPIStub struct {
	ecsdriver.ECSClusterAPI
	observed ecsdriver.ObservedState
	found    bool
	err      error
}

func (s ecsLookupAPIStub) DescribeCluster(context.Context, string) (ecsdriver.ObservedState, bool, error) {
	return s.observed, s.found, s.err
}

type ecrLookupAPIStub struct {
	ecrdriver.RepositoryAPI
	observed ecrdriver.ObservedState
	err      error
}

func (s ecrLookupAPIStub) DescribeRepository(context.Context, string) (ecrdriver.ObservedState, error) {
	return s.observed, s.err
}

type logGroupLookupAPIStub struct {
	logdriver.LogGroupAPI
	observed logdriver.ObservedState
	found    bool
	err      error
}

func (s logGroupLookupAPIStub) DescribeLogGroup(context.Context, string) (logdriver.ObservedState, bool, error) {
	return s.observed, s.found, s.err
}

func TestECSClusterLookupProbe_ByNameAndTag(t *testing.T) {
	probe := ecsClusterLookupProbe(ecsLookupAPIStub{found: true, observed: ecsdriver.ObservedState{
		ARN: "arn:aws:ecs:us-west-2:123:cluster/payments", Name: "payments", Status: "ACTIVE",
		Tags: map[string]string{"environment": "prod"},
	}})
	outputs, found, err := probe(nil, LookupFilter{Name: "payments", Tag: map[string]string{"environment": "prod"}})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "ACTIVE", outputs.Status)
}

func TestECRRepositoryLookupProbe_RejectsMismatchedTag(t *testing.T) {
	probe := ecrRepositoryLookupProbe(ecrLookupAPIStub{observed: ecrdriver.ObservedState{
		RepositoryName: "payments", Tags: map[string]string{"environment": "dev"},
	}})
	_, found, err := probe(nil, LookupFilter{Name: "payments", Tag: map[string]string{"environment": "prod"}})
	require.NoError(t, err)
	assert.False(t, found)
}

func TestLogGroupLookupProbe_MapsOptionalRetention(t *testing.T) {
	retention := int32(30)
	probe := logGroupLookupProbe(logGroupLookupAPIStub{found: true, observed: logdriver.ObservedState{
		ARN: "arn:aws:logs:us-west-2:123:log-group:/praxis/payments", LogGroupName: "/praxis/payments",
		RetentionInDays: &retention, StoredBytes: 1024,
	}})
	outputs, found, err := probe(nil, LookupFilter{ID: "/praxis/payments"})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int32(30), outputs.RetentionInDays)
	assert.Equal(t, int64(1024), outputs.StoredBytes)
}
