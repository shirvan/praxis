package provider

import (
	"context"
	"errors"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/auroracluster"
	"github.com/shirvan/praxis/internal/drivers/dbsubnetgroup"
	"github.com/shirvan/praxis/internal/drivers/iamgroup"
	"github.com/shirvan/praxis/internal/drivers/iaminstanceprofile"
	"github.com/shirvan/praxis/internal/drivers/iampolicy"
	"github.com/shirvan/praxis/internal/drivers/iamuser"
	"github.com/shirvan/praxis/internal/drivers/keypair"
	"github.com/shirvan/praxis/internal/drivers/snstopic"
	"github.com/shirvan/praxis/internal/drivers/sqs"
)

type auroraLookupAPIStub struct {
	auroracluster.AuroraClusterAPI
	observed auroracluster.ObservedState
	err      error
	identity string
}

func (s *auroraLookupAPIStub) DescribeDBCluster(_ context.Context, identity string) (auroracluster.ObservedState, error) {
	s.identity = identity
	return s.observed, s.err
}

type dbSubnetGroupLookupAPIStub struct {
	dbsubnetgroup.DBSubnetGroupAPI
	observed dbsubnetgroup.ObservedState
	err      error
}

func (s *dbSubnetGroupLookupAPIStub) DescribeDBSubnetGroup(context.Context, string) (dbsubnetgroup.ObservedState, error) {
	return s.observed, s.err
}

type snsTopicLookupAPIStub struct {
	snstopic.TopicAPI
	observed snstopic.ObservedState
	findARN  string
	err      error
	findName string
}

func (s *snsTopicLookupAPIStub) FindByName(_ context.Context, name string) (string, error) {
	s.findName = name
	return s.findARN, s.err
}

func (s *snsTopicLookupAPIStub) GetTopicAttributes(context.Context, string) (snstopic.ObservedState, error) {
	return s.observed, s.err
}

type sqsQueueLookupAPIStub struct {
	sqs.QueueAPI
	observed sqs.ObservedState
	queueURL string
	err      error
	findName string
}

func (s *sqsQueueLookupAPIStub) GetQueueUrl(_ context.Context, name string) (string, error) {
	s.findName = name
	return s.queueURL, s.err
}

func (s *sqsQueueLookupAPIStub) GetQueueAttributes(context.Context, string) (sqs.ObservedState, error) {
	return s.observed, s.err
}

type iamPolicyLookupAPIStub struct {
	iampolicy.IAMPolicyAPI
	observed iampolicy.ObservedState
	err      error
	findName string
}

func (s *iamPolicyLookupAPIStub) DescribePolicy(context.Context, string) (iampolicy.ObservedState, error) {
	return s.observed, s.err
}

func (s *iamPolicyLookupAPIStub) DescribePolicyByName(_ context.Context, name, _ string) (iampolicy.ObservedState, error) {
	s.findName = name
	return s.observed, s.err
}

type iamUserLookupAPIStub struct {
	iamuser.IAMUserAPI
	observed iamuser.ObservedState
	err      error
	identity string
}

func (s *iamUserLookupAPIStub) DescribeUser(_ context.Context, identity string) (iamuser.ObservedState, error) {
	s.identity = identity
	return s.observed, s.err
}

type iamGroupLookupAPIStub struct {
	iamgroup.IAMGroupAPI
	observed iamgroup.ObservedState
	err      error
}

func (s *iamGroupLookupAPIStub) DescribeGroup(context.Context, string) (iamgroup.ObservedState, error) {
	return s.observed, s.err
}

type iamInstanceProfileLookupAPIStub struct {
	iaminstanceprofile.IAMInstanceProfileAPI
	observed iaminstanceprofile.ObservedState
	err      error
	identity string
}

func (s *iamInstanceProfileLookupAPIStub) DescribeInstanceProfile(_ context.Context, identity string) (iaminstanceprofile.ObservedState, error) {
	s.identity = identity
	return s.observed, s.err
}

type keyPairLookupAPIStub struct {
	keypair.KeyPairAPI
	observed keypair.ObservedState
	err      error
}

func (s *keyPairLookupAPIStub) DescribeKeyPair(context.Context, string) (keypair.ObservedState, error) {
	return s.observed, s.err
}

func TestLookupBatch_NativeReadsAndOutputMapping(t *testing.T) {
	t.Run("AuroraCluster", func(t *testing.T) {
		api := &auroraLookupAPIStub{observed: auroracluster.ObservedState{
			ClusterIdentifier: "orders", ARN: "arn:aws:rds:us-west-2:123:cluster:orders",
			Endpoint: "orders.writer", ReaderEndpoint: "orders.reader", Port: 5432,
			Engine: "aurora-postgresql", Status: "available", Tags: map[string]string{"env": "prod"},
		}}
		outputs, found, err := auroraClusterLookupProbe(api)(nil, LookupFilter{Name: "orders", Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "orders", api.identity)
		assert.Equal(t, "orders.reader", outputs.ReaderEndpoint)
	})

	t.Run("DBSubnetGroup", func(t *testing.T) {
		api := &dbSubnetGroupLookupAPIStub{observed: dbsubnetgroup.ObservedState{
			GroupName: "private", ARN: "arn:aws:rds:us-west-2:123:subgrp:private", VpcId: "vpc-1",
			SubnetIds: []string{"subnet-1", "subnet-2"}, Status: "Complete", Tags: map[string]string{"env": "prod"},
		}}
		outputs, found, err := dbSubnetGroupLookupProbe(api)(nil, LookupFilter{ID: "private", Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "vpc-1", outputs.VpcId)
	})

	t.Run("SNSTopic", func(t *testing.T) {
		const arn = "arn:aws:sns:us-west-2:123:events"
		api := &snsTopicLookupAPIStub{findARN: arn, observed: snstopic.ObservedState{
			TopicArn: arn, TopicName: "events", Owner: "123", Tags: map[string]string{"env": "prod"},
		}}
		outputs, found, err := snsTopicLookupProbe(api)(nil, LookupFilter{Name: "events", Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "events", api.findName)
		assert.Equal(t, arn, outputs.TopicArn)
	})

	t.Run("SQSQueue", func(t *testing.T) {
		const queueURL = "https://sqs.us-west-2.amazonaws.com/123/events"
		api := &sqsQueueLookupAPIStub{queueURL: queueURL, observed: sqs.ObservedState{
			QueueUrl: queueURL, QueueArn: "arn:aws:sqs:us-west-2:123:events", QueueName: "events",
			Tags: map[string]string{"env": "prod"},
		}}
		outputs, found, err := sqsQueueLookupProbe(api)(nil, LookupFilter{Name: "events", Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "events", api.findName)
		assert.Equal(t, queueURL, outputs.QueueUrl)
	})

	t.Run("IAMPolicy", func(t *testing.T) {
		api := &iamPolicyLookupAPIStub{observed: iampolicy.ObservedState{
			Arn: "arn:aws:iam::123:policy/deploy", PolicyId: "ANPA1", PolicyName: "deploy",
			Tags: map[string]string{"env": "prod"},
		}}
		outputs, found, err := iamPolicyLookupProbe(api)(nil, LookupFilter{Name: "deploy", Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "deploy", api.findName)
		assert.Equal(t, "ANPA1", outputs.PolicyId)
	})

	t.Run("IAMUser ARN", func(t *testing.T) {
		const arn = "arn:aws:iam::123:user/service/deployer"
		api := &iamUserLookupAPIStub{observed: iamuser.ObservedState{
			Arn: arn, UserId: "AIDA1", UserName: "deployer", Tags: map[string]string{"env": "prod"},
		}}
		outputs, found, err := iamUserLookupProbe(api)(nil, LookupFilter{ID: arn, Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "deployer", api.identity)
		assert.Equal(t, "deployer", outputs.UserName)
	})

	t.Run("IAMGroup", func(t *testing.T) {
		api := &iamGroupLookupAPIStub{observed: iamgroup.ObservedState{
			Arn: "arn:aws:iam::123:group/operators", GroupId: "AGPA1", GroupName: "operators",
		}}
		outputs, found, err := iamGroupLookupProbe(api)(nil, LookupFilter{Name: "operators"})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "AGPA1", outputs.GroupId)
	})

	t.Run("IAMInstanceProfile ARN", func(t *testing.T) {
		const arn = "arn:aws:iam::123:instance-profile/service/web"
		api := &iamInstanceProfileLookupAPIStub{observed: iaminstanceprofile.ObservedState{
			Arn: arn, InstanceProfileId: "AIPA1", InstanceProfileName: "web", Tags: map[string]string{"env": "prod"},
		}}
		outputs, found, err := iamInstanceProfileLookupProbe(api)(nil, LookupFilter{ID: arn, Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "web", api.identity)
		assert.Equal(t, "AIPA1", outputs.InstanceProfileId)
	})

	t.Run("KeyPair", func(t *testing.T) {
		api := &keyPairLookupAPIStub{observed: keypair.ObservedState{
			KeyName: "deploy", KeyPairId: "key-1", KeyFingerprint: "fingerprint", KeyType: "ed25519",
			Tags: map[string]string{"env": "prod"},
		}}
		outputs, found, err := keyPairLookupProbe(api)(nil, LookupFilter{Name: "deploy", Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "key-1", outputs.KeyPairId)
		assert.Empty(t, outputs.PrivateKeyMaterial)
	})
}

func TestLookupBatch_TagOnlyIsTerminalValidation(t *testing.T) {
	tagOnly := LookupFilter{Tag: map[string]string{"env": "prod"}}
	tests := map[string]func() error{
		"AuroraCluster": func() error { _, _, err := auroraClusterLookupProbe(&auroraLookupAPIStub{})(nil, tagOnly); return err },
		"DBSubnetGroup": func() error {
			_, _, err := dbSubnetGroupLookupProbe(&dbSubnetGroupLookupAPIStub{})(nil, tagOnly)
			return err
		},
		"SNSTopic":  func() error { _, _, err := snsTopicLookupProbe(&snsTopicLookupAPIStub{})(nil, tagOnly); return err },
		"SQSQueue":  func() error { _, _, err := sqsQueueLookupProbe(&sqsQueueLookupAPIStub{})(nil, tagOnly); return err },
		"IAMPolicy": func() error { _, _, err := iamPolicyLookupProbe(&iamPolicyLookupAPIStub{})(nil, tagOnly); return err },
		"IAMUser":   func() error { _, _, err := iamUserLookupProbe(&iamUserLookupAPIStub{})(nil, tagOnly); return err },
		"IAMGroup":  func() error { _, _, err := iamGroupLookupProbe(&iamGroupLookupAPIStub{})(nil, tagOnly); return err },
		"IAMInstanceProfile": func() error {
			_, _, err := iamInstanceProfileLookupProbe(&iamInstanceProfileLookupAPIStub{})(nil, tagOnly)
			return err
		},
		"KeyPair": func() error { _, _, err := keyPairLookupProbe(&keyPairLookupAPIStub{})(nil, tagOnly); return err },
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			err := run()
			require.Error(t, err)
			assert.True(t, restate.IsTerminalError(err))
			assert.Equal(t, uint16(400), uint16(restate.ErrorCode(err)))
		})
	}
}

func TestLookupBatch_ProviderErrorRemainsRetryable(t *testing.T) {
	want := errors.New("temporary provider failure")
	_, _, err := auroraClusterLookupProbe(&auroraLookupAPIStub{err: want})(nil, LookupFilter{Name: "orders"})
	assert.ErrorIs(t, err, want)
	assert.False(t, restate.IsTerminalError(err))
}

func TestLookupBatch_NotFoundBecomesMissingResult(t *testing.T) {
	outputs, found, err := auroraClusterLookupProbe(&auroraLookupAPIStub{err: errors.New("db cluster not found")})(nil, LookupFilter{Name: "missing"})
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, outputs)
}

func TestLookupBatch_TestConstructorsConfigureLookup(t *testing.T) {
	adapters := map[string]lookupConfigurationConformance{
		"AuroraCluster":      NewAuroraClusterAdapterWithAPI(&auroraLookupAPIStub{}),
		"DBSubnetGroup":      NewDBSubnetGroupAdapterWithAPI(&dbSubnetGroupLookupAPIStub{}),
		"SNSTopic":           NewSNSTopicAdapterWithAPI(&snsTopicLookupAPIStub{}),
		"SQSQueue":           NewSQSAdapterWithAPI(&sqsQueueLookupAPIStub{}),
		"IAMPolicy":          NewIAMPolicyAdapterWithAPI(&iamPolicyLookupAPIStub{}),
		"IAMUser":            NewIAMUserAdapterWithAPI(&iamUserLookupAPIStub{}),
		"IAMGroup":           NewIAMGroupAdapterWithAPI(&iamGroupLookupAPIStub{}),
		"IAMInstanceProfile": NewIAMInstanceProfileAdapterWithAPI(&iamInstanceProfileLookupAPIStub{}),
		"KeyPair":            NewKeyPairAdapterWithAPI(&keyPairLookupAPIStub{}),
	}
	for name, adapter := range adapters {
		t.Run(name, func(t *testing.T) {
			assert.True(t, adapter.lookupConfigured())
		})
	}
}
