package provider

import (
	"context"
	"errors"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dynamodbdriver "github.com/shirvan/praxis/internal/drivers/dynamodbtable"
	ec2driver "github.com/shirvan/praxis/internal/drivers/ec2"
	lambdadriver "github.com/shirvan/praxis/internal/drivers/lambda"
	rdsdriver "github.com/shirvan/praxis/internal/drivers/rdsinstance"
)

type ec2LookupAPIStub struct {
	ec2driver.EC2API
	observed ec2driver.ObservedState
	findID   string
	err      error
}

func (s ec2LookupAPIStub) DescribeInstance(context.Context, string) (ec2driver.ObservedState, error) {
	return s.observed, s.err
}

func (s ec2LookupAPIStub) FindByTags(context.Context, map[string]string) (string, error) {
	return s.findID, s.err
}

type lambdaLookupAPIStub struct {
	lambdadriver.LambdaAPI
	observed lambdadriver.ObservedState
	err      error
}

func (s lambdaLookupAPIStub) DescribeFunction(context.Context, string) (lambdadriver.ObservedState, error) {
	return s.observed, s.err
}

type dynamoLookupAPIStub struct {
	dynamodbdriver.DynamoDBTableAPI
	observed dynamodbdriver.ObservedState
	found    bool
	err      error
}

func (s dynamoLookupAPIStub) DescribeTable(context.Context, string) (dynamodbdriver.ObservedState, bool, error) {
	return s.observed, s.found, s.err
}

type rdsLookupAPIStub struct {
	rdsdriver.RDSInstanceAPI
	observed rdsdriver.ObservedState
	err      error
}

func (s rdsLookupAPIStub) DescribeDBInstance(context.Context, string) (rdsdriver.ObservedState, error) {
	return s.observed, s.err
}

func TestEC2LookupProbe_ByNameAndTags(t *testing.T) {
	probe := ec2LookupProbe(ec2LookupAPIStub{
		findID: "i-123",
		observed: ec2driver.ObservedState{
			InstanceId: "i-123", State: "running", PrivateIpAddress: "10.0.0.4",
			Tags: map[string]string{"Name": "payments", "env": "prod"},
		},
	})
	outputs, found, err := probe(nil, LookupFilter{Name: "payments", Tag: map[string]string{"env": "prod"}})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "i-123", outputs.InstanceId)
	assert.Equal(t, "10.0.0.4", outputs.PrivateIpAddress)
}

func TestEC2LookupProbe_RejectsMismatchedTagAfterDescribe(t *testing.T) {
	probe := ec2LookupProbe(ec2LookupAPIStub{
		observed: ec2driver.ObservedState{InstanceId: "i-123", State: "running", Tags: map[string]string{"env": "dev"}},
	})
	_, found, err := probe(nil, LookupFilter{ID: "i-123", Tag: map[string]string{"env": "prod"}})
	require.NoError(t, err)
	assert.False(t, found)
}

func TestLambdaLookupProbe_ByName(t *testing.T) {
	probe := lambdaLookupProbe(lambdaLookupAPIStub{observed: lambdadriver.ObservedState{
		FunctionArn:  "arn:aws:lambda:us-west-2:123:function:payments",
		FunctionName: "payments", State: "Active", Tags: map[string]string{"env": "prod"},
	}})
	outputs, found, err := probe(nil, LookupFilter{Name: "payments", Tag: map[string]string{"env": "prod"}})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "payments", outputs.FunctionName)
	assert.Equal(t, "Active", outputs.State)
}

func TestLambdaLookupProbe_TagOnlyIsTerminalValidation(t *testing.T) {
	probe := lambdaLookupProbe(lambdaLookupAPIStub{})
	_, _, err := probe(nil, LookupFilter{Tag: map[string]string{"env": "prod"}})
	require.Error(t, err)
	assert.True(t, restate.IsTerminalError(err))
	assert.Equal(t, uint16(400), uint16(restate.ErrorCode(err)))
}

func TestDynamoDBTableLookupProbe_ByName(t *testing.T) {
	probe := dynamoDBTableLookupProbe(dynamoLookupAPIStub{found: true, observed: dynamodbdriver.ObservedState{
		ARN: "arn:aws:dynamodb:us-west-2:123:table/payments", Name: "payments", Status: "ACTIVE", ItemCount: 42,
		Tags: map[string]string{"env": "prod"},
	}})
	outputs, found, err := probe(nil, LookupFilter{Name: "payments", Tag: map[string]string{"env": "prod"}})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(42), outputs.ItemCount)
}

func TestRDSInstanceLookupProbe_ByIdentifier(t *testing.T) {
	probe := rdsInstanceLookupProbe(rdsLookupAPIStub{observed: rdsdriver.ObservedState{
		DBIdentifier: "payments", ARN: "arn:aws:rds:us-west-2:123:db:payments",
		Endpoint: "payments.example", Port: 5432, Status: "available", Tags: map[string]string{"env": "prod"},
	}})
	outputs, found, err := probe(nil, LookupFilter{ID: "payments", Tag: map[string]string{"env": "prod"}})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "payments.example", outputs.Endpoint)
	assert.Equal(t, int32(5432), outputs.Port)
}

func TestNativeLookupProbe_ProviderErrorRemainsRetryable(t *testing.T) {
	want := errors.New("temporary provider failure")
	probe := rdsInstanceLookupProbe(rdsLookupAPIStub{err: want})
	_, _, err := probe(nil, LookupFilter{ID: "payments"})
	assert.ErrorIs(t, err, want)
}
