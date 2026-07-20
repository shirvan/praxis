package provider

import (
	"context"
	"errors"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/acmcert"
	"github.com/shirvan/praxis/internal/drivers/esm"
	"github.com/shirvan/praxis/internal/drivers/listener"
	"github.com/shirvan/praxis/internal/drivers/listenerrule"
	"github.com/shirvan/praxis/internal/drivers/snssub"
	"github.com/shirvan/praxis/internal/drivers/sqspolicy"
)

type acmCertificateLookupAPIStub struct {
	acmcert.CertificateAPI
	observed acmcert.ObservedState
	err      error
}

func (s *acmCertificateLookupAPIStub) DescribeCertificate(context.Context, string) (acmcert.ObservedState, error) {
	return s.observed, s.err
}

type esmDirectLookupAPIStub struct {
	esm.ESMAPI
	observed esm.ObservedState
	err      error
}

func (s *esmDirectLookupAPIStub) GetEventSourceMapping(context.Context, string) (esm.ObservedState, error) {
	return s.observed, s.err
}

type listenerDirectLookupAPIStub struct {
	listener.ListenerAPI
	observed listener.ObservedState
	err      error
}

func (s *listenerDirectLookupAPIStub) DescribeListener(context.Context, string) (listener.ObservedState, error) {
	return s.observed, s.err
}

type listenerRuleDirectLookupAPIStub struct {
	listenerrule.ListenerRuleAPI
	observed listenerrule.ObservedState
	err      error
}

func (s *listenerRuleDirectLookupAPIStub) DescribeRule(context.Context, string) (listenerrule.ObservedState, error) {
	return s.observed, s.err
}

type snsSubscriptionDirectLookupAPIStub struct {
	snssub.SubscriptionAPI
	observed snssub.ObservedState
	err      error
}

func (s *snsSubscriptionDirectLookupAPIStub) GetSubscriptionAttributes(context.Context, string) (snssub.ObservedState, error) {
	return s.observed, s.err
}

type sqsQueuePolicyDirectLookupAPIStub struct {
	sqspolicy.PolicyAPI
	observed sqspolicy.ObservedState
	err      error
}

func (s *sqsQueuePolicyDirectLookupAPIStub) GetQueuePolicy(context.Context, string) (sqspolicy.ObservedState, error) {
	return s.observed, s.err
}

func TestDirectIdentityLookupBatch_MapsOutputs(t *testing.T) {
	t.Run("ACMCertificate", func(t *testing.T) {
		const arn = "arn:aws:acm:us-west-2:123:certificate/cert-1"
		outputs, found, err := acmCertificateLookupProbe(&acmCertificateLookupAPIStub{observed: acmcert.ObservedState{
			CertificateArn: arn, DomainName: "example.com", Status: "ISSUED", NotAfter: "2027-01-01T00:00:00Z",
		}})(nil, LookupFilter{ID: arn})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "example.com", outputs.DomainName)
		assert.Equal(t, "ISSUED", outputs.Status)
	})

	t.Run("EventSourceMapping", func(t *testing.T) {
		outputs, found, err := esmLookupProbe(&esmDirectLookupAPIStub{observed: esm.ObservedState{
			UUID: "uuid-1", EventSourceArn: "arn:aws:sqs:us-west-2:123:events",
			FunctionArn: "arn:aws:lambda:us-west-2:123:function:worker", State: "Enabled", BatchSize: 10,
		}})(nil, LookupFilter{ID: "uuid-1"})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, int32(10), outputs.BatchSize)
		assert.Equal(t, "Enabled", outputs.State)
	})

	t.Run("Listener", func(t *testing.T) {
		const arn = "arn:aws:elasticloadbalancing:us-west-2:123:listener/app/lb/1/2"
		outputs, found, err := listenerLookupProbe(&listenerDirectLookupAPIStub{observed: listener.ObservedState{
			ListenerArn: arn, Port: 443, Protocol: "HTTPS",
		}})(nil, LookupFilter{ID: arn})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, 443, outputs.Port)
		assert.Equal(t, "HTTPS", outputs.Protocol)
	})

	t.Run("ListenerRule", func(t *testing.T) {
		const arn = "arn:aws:elasticloadbalancing:us-west-2:123:listener-rule/app/lb/1/2/3"
		outputs, found, err := listenerRuleLookupProbe(&listenerRuleDirectLookupAPIStub{observed: listenerrule.ObservedState{
			RuleArn: arn, Priority: 100,
		}})(nil, LookupFilter{ID: arn})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, 100, outputs.Priority)
	})

	t.Run("SNSSubscription", func(t *testing.T) {
		const arn = "arn:aws:sns:us-west-2:123:events:sub-1"
		outputs, found, err := snsSubscriptionLookupProbe(&snsSubscriptionDirectLookupAPIStub{observed: snssub.ObservedState{
			SubscriptionArn: arn, TopicArn: "arn:aws:sns:us-west-2:123:events",
			Protocol: "sqs", Endpoint: "arn:aws:sqs:us-west-2:123:worker", Owner: "123",
		}})(nil, LookupFilter{ID: arn})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "sqs", outputs.Protocol)
		assert.Equal(t, "123", outputs.Owner)
	})

	t.Run("SQSQueuePolicy", func(t *testing.T) {
		const queueURL = "https://sqs.us-west-2.amazonaws.com/123/events"
		outputs, found, err := sqsQueuePolicyLookupProbe(&sqsQueuePolicyDirectLookupAPIStub{observed: sqspolicy.ObservedState{
			QueueUrl: queueURL, QueueArn: "arn:aws:sqs:us-west-2:123:events", Policy: `{"Version":"2012-10-17"}`,
		}})(nil, LookupFilter{ID: queueURL})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "events", outputs.QueueName)
	})
}

func TestDirectIdentityLookupBatch_RejectsUnsupportedSelectors(t *testing.T) {
	tests := map[string]func(LookupFilter) error{
		"ACMCertificate": func(filter LookupFilter) error {
			_, _, err := acmCertificateLookupProbe(&acmCertificateLookupAPIStub{})(nil, filter)
			return err
		},
		"EventSourceMapping": func(filter LookupFilter) error {
			_, _, err := esmLookupProbe(&esmDirectLookupAPIStub{})(nil, filter)
			return err
		},
		"Listener": func(filter LookupFilter) error {
			_, _, err := listenerLookupProbe(&listenerDirectLookupAPIStub{})(nil, filter)
			return err
		},
		"ListenerRule": func(filter LookupFilter) error {
			_, _, err := listenerRuleLookupProbe(&listenerRuleDirectLookupAPIStub{})(nil, filter)
			return err
		},
		"SNSSubscription": func(filter LookupFilter) error {
			_, _, err := snsSubscriptionLookupProbe(&snsSubscriptionDirectLookupAPIStub{})(nil, filter)
			return err
		},
		"SQSQueuePolicy": func(filter LookupFilter) error {
			_, _, err := sqsQueuePolicyLookupProbe(&sqsQueuePolicyDirectLookupAPIStub{})(nil, filter)
			return err
		},
	}
	selectors := map[string]LookupFilter{
		"name": {Name: "unsupported"},
		"tag":  {Tag: map[string]string{"env": "prod"}},
	}
	for kind, run := range tests {
		for selector, filter := range selectors {
			t.Run(kind+"/"+selector, func(t *testing.T) {
				err := run(filter)
				require.Error(t, err)
				assert.True(t, restate.IsTerminalError(err))
				assert.Equal(t, uint16(400), uint16(restate.ErrorCode(err)))
			})
		}
	}
}

func TestDirectIdentityLookupBatch_SQSQueueWithoutPolicyIsMissing(t *testing.T) {
	const queueURL = "https://sqs.us-west-2.amazonaws.com/123/events"
	outputs, found, err := sqsQueuePolicyLookupProbe(&sqsQueuePolicyDirectLookupAPIStub{observed: sqspolicy.ObservedState{
		QueueUrl: queueURL, QueueArn: "arn:aws:sqs:us-west-2:123:events",
	}})(nil, LookupFilter{ID: queueURL})
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, outputs)
}

func TestDirectIdentityLookupBatch_NotFoundBecomesMissingResult(t *testing.T) {
	const arn = "arn:aws:acm:us-west-2:123:certificate/missing"
	outputs, found, err := acmCertificateLookupProbe(&acmCertificateLookupAPIStub{err: errors.New("certificate not found")})(nil, LookupFilter{ID: arn})
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, outputs)
}

func TestDirectIdentityLookupBatch_ProviderErrorRemainsRetryable(t *testing.T) {
	want := errors.New("temporary provider failure")
	_, _, err := listenerLookupProbe(&listenerDirectLookupAPIStub{err: want})(nil, LookupFilter{ID: "listener-arn"})
	assert.ErrorIs(t, err, want)
	assert.False(t, restate.IsTerminalError(err))
}

func TestDirectIdentityLookupBatch_TestConstructorsConfigureLookup(t *testing.T) {
	adapters := map[string]lookupConfigurationConformance{
		"ACMCertificate":     NewACMCertificateAdapterWithAPI(&acmCertificateLookupAPIStub{}),
		"EventSourceMapping": NewESMAdapterWithAPI(&esmDirectLookupAPIStub{}),
		"Listener":           NewListenerAdapterWithAPI(&listenerDirectLookupAPIStub{}),
		"ListenerRule":       NewListenerRuleAdapterWithAPI(&listenerRuleDirectLookupAPIStub{}),
		"SNSSubscription":    NewSNSSubscriptionAdapterWithAPI(&snsSubscriptionDirectLookupAPIStub{}),
		"SQSQueuePolicy":     NewSQSQueuePolicyAdapterWithAPI(&sqsQueuePolicyDirectLookupAPIStub{}),
	}
	for name, adapter := range adapters {
		t.Run(name, func(t *testing.T) {
			assert.True(t, adapter.lookupConfigured())
		})
	}
}
