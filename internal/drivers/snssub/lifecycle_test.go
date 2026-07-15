package snssub

import (
	"context"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type subscriptionAttributeCall struct {
	name  string
	value string
}

// fakeSubscriptionAPI retains provider state so repeated Provision calls test
// convergence against fresh observations, including explicit removals.
type fakeSubscriptionAPI struct {
	mu sync.Mutex

	observed       ObservedState
	attributeCalls []subscriptionAttributeCall
	subscribeCalls int
	getCalls       int
	deleteCalls    int
}

func (f *fakeSubscriptionAPI) Subscribe(_ context.Context, spec SNSSubscriptionSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscribeCalls++
	if f.observed.SubscriptionArn != "" && f.observed.TopicArn == spec.TopicArn &&
		f.observed.Protocol == spec.Protocol && f.observed.Endpoint == spec.Endpoint {
		return f.observed.SubscriptionArn, nil
	}
	arn := "arn:aws:sns:us-east-1:123456789012:alerts:subscription-id"
	scope := spec.FilterPolicyScope
	if scope == "" {
		scope = "MessageAttributes"
	}
	f.observed = ObservedState{
		SubscriptionArn:     arn,
		TopicArn:            spec.TopicArn,
		Protocol:            spec.Protocol,
		Endpoint:            spec.Endpoint,
		Owner:               "123456789012",
		FilterPolicy:        spec.FilterPolicy,
		FilterPolicyScope:   scope,
		RawMessageDelivery:  spec.RawMessageDelivery,
		DeliveryPolicy:      spec.DeliveryPolicy,
		RedrivePolicy:       spec.RedrivePolicy,
		SubscriptionRoleArn: spec.SubscriptionRoleArn,
		ConfirmationStatus:  "confirmed",
	}
	return arn, nil
}

func (f *fakeSubscriptionAPI) GetSubscriptionAttributes(_ context.Context, subscriptionArn string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.observed.SubscriptionArn != subscriptionArn {
		return ObservedState{}, &mockAPIError{code: "NotFound", message: "missing subscription"}
	}
	return f.observed, nil
}

func (f *fakeSubscriptionAPI) SetSubscriptionAttribute(_ context.Context, subscriptionArn, attrName, attrValue string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.SubscriptionArn != subscriptionArn {
		return &mockAPIError{code: "NotFound", message: "missing subscription"}
	}
	f.attributeCalls = append(f.attributeCalls, subscriptionAttributeCall{name: attrName, value: attrValue})
	switch attrName {
	case "FilterPolicy":
		if isEmptyJSONObject(attrValue) {
			f.observed.FilterPolicy = ""
		} else {
			f.observed.FilterPolicy = attrValue
		}
	case "FilterPolicyScope":
		f.observed.FilterPolicyScope = attrValue
	case "RawMessageDelivery":
		f.observed.RawMessageDelivery = attrValue == "true"
	case "DeliveryPolicy":
		if isEmptyJSONObject(attrValue) {
			f.observed.DeliveryPolicy = ""
		} else {
			f.observed.DeliveryPolicy = attrValue
		}
	case "RedrivePolicy":
		if isEmptyJSONObject(attrValue) {
			f.observed.RedrivePolicy = ""
		} else {
			f.observed.RedrivePolicy = attrValue
		}
	case "SubscriptionRoleArn":
		f.observed.SubscriptionRoleArn = attrValue
	}
	return nil
}

func (f *fakeSubscriptionAPI) Unsubscribe(_ context.Context, subscriptionArn string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	if f.observed.SubscriptionArn == subscriptionArn {
		f.observed = ObservedState{}
	}
	return nil
}

func (f *fakeSubscriptionAPI) FindByTopicProtocolEndpoint(_ context.Context, topicArn, protocol, endpoint string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.TopicArn == topicArn && f.observed.Protocol == protocol && f.observed.Endpoint == endpoint {
		return f.observed.SubscriptionArn, nil
	}
	return "", nil
}

func (f *fakeSubscriptionAPI) resetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attributeCalls = nil
}

func setupSubscriptionDriver(t *testing.T, api SubscriptionAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := NewSNSSubscriptionDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) SubscriptionAPI {
		return api
	})
	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress()
}

func provisionSubscription(t *testing.T, client *ingress.Client, key string, spec SNSSubscriptionSpec) SNSSubscriptionOutputs {
	t.Helper()
	outputs, err := ingress.Object[SNSSubscriptionSpec, SNSSubscriptionOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	return outputs
}

func subscriptionBaseSpec() SNSSubscriptionSpec {
	return SNSSubscriptionSpec{
		Account:  "test",
		Region:   "us-east-1",
		TopicArn: "arn:aws:sns:us-east-1:123456789012:alerts",
		Protocol: "http",
		Endpoint: "http://example.com/alerts",
	}
}

func TestProvisionExistingSubscriptionRemovesOmittedOptionalConfiguration(t *testing.T) {
	desired := subscriptionBaseSpec()
	api := &fakeSubscriptionAPI{observed: ObservedState{
		SubscriptionArn:     "arn:aws:sns:us-east-1:123456789012:alerts:subscription-id",
		TopicArn:            desired.TopicArn,
		Protocol:            desired.Protocol,
		Endpoint:            desired.Endpoint,
		Owner:               "123456789012",
		FilterPolicy:        `{"event":["order"]}`,
		FilterPolicyScope:   "MessageBody",
		RawMessageDelivery:  true,
		DeliveryPolicy:      `{"healthyRetryPolicy":{"numRetries":3}}`,
		RedrivePolicy:       `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:123456789012:dlq"}`,
		SubscriptionRoleArn: "arn:aws:iam::123456789012:role/old",
		ConfirmationStatus:  "confirmed",
	}}
	client := setupSubscriptionDriver(t, api)
	key := "subscription-key"
	provisionSubscription(t, client, key, desired)

	api.mu.Lock()
	defer api.mu.Unlock()
	values := make(map[string]string, len(api.attributeCalls))
	for _, call := range api.attributeCalls {
		values[call.name] = call.value
	}
	assert.Equal(t, "{}", values["FilterPolicy"])
	assert.Equal(t, "MessageAttributes", values["FilterPolicyScope"])
	assert.Equal(t, "false", values["RawMessageDelivery"])
	assert.Equal(t, "{}", values["DeliveryPolicy"])
	assert.Equal(t, "{}", values["RedrivePolicy"])
	assert.Equal(t, "", values["SubscriptionRoleArn"])
	assert.False(t, HasDrift(desired, api.observed))
}

func TestProvisionExistingSubscriptionSkipsSemanticallyEqualJSON(t *testing.T) {
	api := &fakeSubscriptionAPI{}
	client := setupSubscriptionDriver(t, api)
	key := "subscription-key"
	configured := subscriptionBaseSpec()
	configured.FilterPolicy = `{"event":["order"]}`
	configured.DeliveryPolicy = `{"healthyRetryPolicy":{"numRetries":3}}`
	configured.RedrivePolicy = `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:123456789012:dlq"}`
	provisionSubscription(t, client, key, configured)
	api.resetCalls()
	configured.FilterPolicy = `{ "event": ["order"] }`
	configured.DeliveryPolicy = `{ "healthyRetryPolicy": {"numRetries": 3} }`
	configured.RedrivePolicy = `{ "deadLetterTargetArn": "arn:aws:sqs:us-east-1:123456789012:dlq" }`
	provisionSubscription(t, client, key, configured)

	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Empty(t, api.attributeCalls)
}

func TestDeleteSubscriptionKeepsTombstoneAndReconcileCannotResurrect(t *testing.T) {
	api := &fakeSubscriptionAPI{}
	client := setupSubscriptionDriver(t, api)
	key := "subscription-key"
	provisionSubscription(t, client, key, subscriptionBaseSpec())

	_, err := ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusDeleted, status.Status)
	outputs, err := ingress.Object[restate.Void, SNSSubscriptionOutputs](client, ServiceName, key, "GetOutputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Empty(t, outputs.SubscriptionArn)

	api.mu.Lock()
	getCallsAfterDelete := api.getCalls
	api.mu.Unlock()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ReconcileResult{}, result)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Equal(t, getCallsAfterDelete, api.getCalls)
	assert.Equal(t, 1, api.deleteCalls)
}
