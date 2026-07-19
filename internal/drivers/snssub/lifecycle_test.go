package snssub

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	"github.com/shirvan/praxis/internal/eventing"
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
	creations      int

	pending            bool
	pendingSentinel    bool
	createWithoutAttrs bool
	ambiguousSubscribe bool
	failAttributeOnce  bool
	getErrors          []error
}

type subscriptionDriftSink struct{}

func (subscriptionDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }
func (subscriptionDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error {
	return nil
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
	f.creations++
	scope := spec.FilterPolicyScope
	if scope == "" {
		scope = "MessageAttributes"
	}
	observed := ObservedState{
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
	if f.createWithoutAttrs {
		observed.FilterPolicy = ""
		observed.FilterPolicyScope = "MessageAttributes"
		observed.RawMessageDelivery = false
		observed.DeliveryPolicy = ""
		observed.RedrivePolicy = ""
		observed.SubscriptionRoleArn = ""
	}
	if f.pending {
		observed.PendingConfirmation = true
		observed.ConfirmationStatus = "pending"
	}
	f.observed = observed
	if f.ambiguousSubscribe {
		f.ambiguousSubscribe = false
		return "", errors.New("ServiceUnavailable: response lost after Subscribe")
	}
	if f.pendingSentinel {
		return "PendingConfirmation", nil
	}
	return arn, nil
}

func (f *fakeSubscriptionAPI) GetSubscriptionAttributes(_ context.Context, subscriptionArn string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if len(f.getErrors) > 0 {
		err := f.getErrors[0]
		f.getErrors = f.getErrors[1:]
		return ObservedState{}, err
	}
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
	if f.failAttributeOnce {
		f.failAttributeOnce = false
		return &mockAPIError{code: "InvalidParameter", message: "injected attribute failure"}
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
		if f.observed.PendingConfirmation && f.pendingSentinel {
			return "PendingConfirmation", nil
		}
		return f.observed.SubscriptionArn, nil
	}
	return "", nil
}

func (f *fakeSubscriptionAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{
		Creates: f.creations, Reads: f.getCalls, Updates: len(f.attributeCalls), Deletes: f.deleteCalls,
	}
}

func (f *fakeSubscriptionAPI) confirmWithDrift() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = false
	f.pendingSentinel = false
	f.observed.PendingConfirmation = false
	f.observed.ConfirmationStatus = "confirmed"
	f.observed.FilterPolicy = `{"event":["wrong"]}`
	f.observed.RawMessageDelivery = false
}

func (f *fakeSubscriptionAPI) removeExternally() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed = ObservedState{}
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
	driver := newGenericSNSSubscriptionDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) SubscriptionAPI {
		return api
	})
	env := restatetest.Start(t, restate.Reflect(driver), restate.Reflect(subscriptionDriftSink{}))
	return env.Ingress()
}

func provisionSubscription(t *testing.T, client *ingress.Client, key string, spec SNSSubscriptionSpec) SNSSubscriptionOutputs {
	t.Helper()
	outputs, err := ingress.Object[types.ProvisionRequest, SNSSubscriptionOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
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

func TestGenericSNSSubscriptionCoreLifecycle(t *testing.T) {
	api := &fakeSubscriptionAPI{}
	client := setupSubscriptionDriver(t, api)
	spec := subscriptionBaseSpec()
	spec.FilterPolicy = `{"event":["order"]}`
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[SNSSubscriptionSpec, SNSSubscriptionOutputs]{
		Client: client, ServiceName: ServiceName, Key: "core-subscription", Spec: spec, Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs SNSSubscriptionSpec) {
			assert.Equal(t, "us-east-1", inputs.Region)
			assert.Equal(t, spec.TopicArn, inputs.TopicArn)
		},
	})
}

func TestGenericSNSSubscriptionObservedImportLifecycle(t *testing.T) {
	spec := subscriptionBaseSpec()
	api := &fakeSubscriptionAPI{}
	_, err := api.Subscribe(t.Context(), spec)
	require.NoError(t, err)
	client := setupSubscriptionDriver(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[SNSSubscriptionOutputs]{
		Client: client, ServiceName: ServiceName, Key: "observed-subscription",
		Ref: types.ImportRef{
			ResourceID: "arn:aws:sns:us-east-1:123456789012:alerts:subscription-id", Account: "test",
		},
		Snapshot: api.snapshot,
	})
}

func TestGenericSNSSubscriptionPendingTransitionsToReadyAndConverges(t *testing.T) {
	api := &fakeSubscriptionAPI{pending: true, createWithoutAttrs: true}
	client := setupSubscriptionDriver(t, api)
	spec := subscriptionBaseSpec()
	spec.FilterPolicy = `{"event":["order"]}`
	spec.RawMessageDelivery = true
	key := "pending-subscription"
	outputs := provisionSubscription(t, client, key, spec)
	assert.NotEmpty(t, outputs.SubscriptionArn)
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusPending, status.Status)
	assert.Zero(t, api.snapshot().Updates, "attributes cannot be converged before confirmation")

	api.confirmWithDrift()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	status, err = ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.True(t, filterPoliciesEqual(spec.FilterPolicy, api.observed.FilterPolicy))
	assert.True(t, api.observed.RawMessageDelivery)
}

func TestGenericSNSSubscriptionRepeatedProvisionDoesNotDuplicatePendingRequest(t *testing.T) {
	api := &fakeSubscriptionAPI{pending: true, pendingSentinel: true}
	client := setupSubscriptionDriver(t, api)
	spec := subscriptionBaseSpec()
	key := "pending-idempotent"
	first := provisionSubscription(t, client, key, spec)
	second := provisionSubscription(t, client, key, spec)
	assert.Equal(t, first, second)
	assert.Equal(t, types.StatusPending, subscriptionStatus(t, client, key).Status)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Equal(t, 1, api.subscribeCalls)
	assert.Equal(t, 1, api.creations)
}

func TestGenericSNSSubscriptionDeleteProvisionedPendingARNCallsProvider(t *testing.T) {
	api := &fakeSubscriptionAPI{pending: true}
	client := setupSubscriptionDriver(t, api)
	key := "pending-delete"
	provisionSubscription(t, client, key, subscriptionBaseSpec())
	assert.Equal(t, types.StatusPending, subscriptionStatus(t, client, key).Status)

	_, err := ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, 1, api.snapshot().Deletes)
	assert.Equal(t, types.StatusDeleted, subscriptionStatus(t, client, key).Status)
}

func TestGenericSNSSubscriptionDeleteNeverProvisionedIsProviderSilent(t *testing.T) {
	api := &fakeSubscriptionAPI{}
	client := setupSubscriptionDriver(t, api)
	key := "never-provisioned-delete"
	_, err := ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, drivertest.ProviderSnapshot{}, api.snapshot())
	assert.Equal(t, types.StatusDeleted, subscriptionStatus(t, client, key).Status)
}

func TestGenericSNSSubscriptionDeletePendingSentinelConflictsWithoutTombstone(t *testing.T) {
	api := &fakeSubscriptionAPI{pending: true, pendingSentinel: true}
	client := setupSubscriptionDriver(t, api)
	key := "pending-sentinel-delete"
	outputs := provisionSubscription(t, client, key, subscriptionBaseSpec())
	assert.Equal(t, "PendingConfirmation", outputs.SubscriptionArn)
	assert.Equal(t, types.StatusPending, subscriptionStatus(t, client, key).Status)

	_, err := ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AWS has not assigned a subscription ARN")
	assert.Zero(t, api.snapshot().Deletes)
	assert.Equal(t, types.StatusError, subscriptionStatus(t, client, key).Status)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.True(t, api.observed.PendingConfirmation, "the provider request still exists and must not be represented as deleted")
}

func TestGenericSNSSubscriptionRecoversPartialCreateWithoutSecondSubscription(t *testing.T) {
	api := &fakeSubscriptionAPI{createWithoutAttrs: true, failAttributeOnce: true}
	client := setupSubscriptionDriver(t, api)
	spec := subscriptionBaseSpec()
	spec.FilterPolicy = `{"event":["order"]}`
	key := "partial-subscription"
	_, err := ingress.Object[types.ProvisionRequest, SNSSubscriptionOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)

	_, err = ingress.Object[types.ProvisionRequest, SNSSubscriptionOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.True(t, filterPoliciesEqual(spec.FilterPolicy, api.observed.FilterPolicy))
}

func TestGenericSNSSubscriptionRetriesAmbiguousSubscribeByTuple(t *testing.T) {
	api := &fakeSubscriptionAPI{ambiguousSubscribe: true}
	client := setupSubscriptionDriver(t, api)
	outputs := provisionSubscription(t, client, "ambiguous-subscription", subscriptionBaseSpec())
	assert.NotEmpty(t, outputs.SubscriptionArn)
	assert.Equal(t, 1, api.snapshot().Creates)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.GreaterOrEqual(t, api.subscribeCalls, 2)
}

func TestGenericSNSSubscriptionRetriesTransientObservation(t *testing.T) {
	spec := subscriptionBaseSpec()
	api := &fakeSubscriptionAPI{getErrors: []error{errors.New("ServiceUnavailable: transient GetSubscriptionAttributes failure")}}
	_, err := api.Subscribe(t.Context(), spec)
	require.NoError(t, err)
	client := setupSubscriptionDriver(t, api)
	outputs := provisionSubscription(t, client, "read-retry", spec)
	assert.NotEmpty(t, outputs.SubscriptionArn)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.GreaterOrEqual(t, api.snapshot().Reads, 2)
}

func TestGenericSNSSubscriptionExternalDeleteRequiresReplacementWithoutCreate(t *testing.T) {
	api := &fakeSubscriptionAPI{}
	client := setupSubscriptionDriver(t, api)
	key := "external-delete"
	provisionSubscription(t, client, key, subscriptionBaseSpec())
	creates := api.snapshot().Creates
	api.removeExternally()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "SNSSubscription resource was deleted externally")
	assert.Equal(t, creates, api.snapshot().Creates)
}

func TestGenericSNSSubscriptionRejectsImmutableIdentityChanges(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*SNSSubscriptionSpec)
		wantError string
	}{
		{
			name: "topic ARN", wantError: "topicArn is immutable",
			mutate: func(spec *SNSSubscriptionSpec) { spec.TopicArn = "arn:aws:sns:us-east-1:123456789012:other" },
		},
		{
			name: "protocol", wantError: "protocol is immutable",
			mutate: func(spec *SNSSubscriptionSpec) {
				spec.Protocol = "lambda"
				spec.Endpoint = "arn:aws:lambda:us-east-1:123456789012:function:alerts"
			},
		},
		{
			name: "endpoint", wantError: "endpoint is immutable",
			mutate: func(spec *SNSSubscriptionSpec) { spec.Endpoint = "http://example.com/other" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeSubscriptionAPI{}
			client := setupSubscriptionDriver(t, api)
			key := "immutable-" + strings.ReplaceAll(tt.name, " ", "-")
			spec := subscriptionBaseSpec()
			provisionSubscription(t, client, key, spec)
			before := api.snapshot()
			tt.mutate(&spec)

			_, err := ingress.Object[types.ProvisionRequest, SNSSubscriptionOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantError)
			assert.Contains(t, err.Error(), "409")
			after := api.snapshot()
			assert.Equal(t, before.Creates, after.Creates)
			assert.Equal(t, before.Updates, after.Updates)
			assert.Equal(t, before.Deletes, after.Deletes)
		})
	}
}

func subscriptionStatus(t *testing.T, client *ingress.Client, key string) types.StatusResponse {
	t.Helper()
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return status
}
