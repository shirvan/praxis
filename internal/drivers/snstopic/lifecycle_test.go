package snstopic

import (
	"context"
	"errors"
	"maps"
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

type topicAttributeCall struct {
	name  string
	value string
}

// fakeTopicAPI is a stateful provider double. Handler tests use it to verify
// the complete public driver lifecycle rather than only pure drift helpers.
type fakeTopicAPI struct {
	mu sync.Mutex

	observed           ObservedState
	attributeCalls     []topicAttributeCall
	tagCalls           []map[string]string
	createCalls        int
	getCalls           int
	findCalls          int
	subscriptionChecks int
	deleteCalls        int
	hasSubscriptions   bool
	createResponseLost bool
	failAttributeOnce  bool
	failTagOnce        bool
}

type topicDriftSink struct{}

func (topicDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }

func (topicDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

func (f *fakeTopicAPI) CreateTopic(_ context.Context, spec SNSTopicSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	if f.observed.TopicArn != "" && f.observed.TopicName == spec.TopicName {
		return f.observed.TopicArn, nil
	}
	arn := "arn:aws:sns:us-east-1:123456789012:" + spec.TopicName
	f.observed = ObservedState{
		TopicArn:                  arn,
		TopicName:                 spec.TopicName,
		DisplayName:               spec.DisplayName,
		FifoTopic:                 spec.FifoTopic,
		ContentBasedDeduplication: spec.ContentBasedDeduplication,
		Policy:                    spec.Policy,
		DeliveryPolicy:            spec.DeliveryPolicy,
		KmsMasterKeyId:            spec.KmsMasterKeyId,
		Owner:                     "123456789012",
		Tags:                      map[string]string{},
	}
	if f.observed.Policy == "" {
		policy, err := defaultTopicPolicy(f.observed)
		if err != nil {
			return "", err
		}
		f.observed.Policy = policy
	}
	if f.createResponseLost {
		f.createResponseLost = false
		return "", errors.New("ServiceUnavailable: response lost after topic creation")
	}
	return arn, nil
}

func (f *fakeTopicAPI) GetTopicAttributes(_ context.Context, topicArn string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.observed.TopicArn != topicArn {
		return ObservedState{}, &mockAPIError{code: "NotFound", message: "missing topic"}
	}
	return cloneTopicObserved(f.observed), nil
}

func (f *fakeTopicAPI) SetTopicAttribute(_ context.Context, topicArn, attrName, attrValue string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.TopicArn != topicArn {
		return &mockAPIError{code: "NotFound", message: "missing topic"}
	}
	if f.failAttributeOnce {
		f.failAttributeOnce = false
		return &mockAPIError{code: "InvalidParameter", message: "injected partial-completion fault"}
	}
	f.attributeCalls = append(f.attributeCalls, topicAttributeCall{name: attrName, value: attrValue})
	switch attrName {
	case "DisplayName":
		f.observed.DisplayName = attrValue
	case "Policy":
		f.observed.Policy = attrValue
	case "DeliveryPolicy":
		if isEmptyJSONObject(attrValue) {
			f.observed.DeliveryPolicy = ""
		} else {
			f.observed.DeliveryPolicy = attrValue
		}
	case "KmsMasterKeyId":
		f.observed.KmsMasterKeyId = attrValue
	case "ContentBasedDeduplication":
		f.observed.ContentBasedDeduplication = attrValue == "true"
	}
	return nil
}

func (f *fakeTopicAPI) DeleteTopic(_ context.Context, topicArn string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	if f.observed.TopicArn == topicArn {
		f.observed = ObservedState{}
	}
	return nil
}

func (f *fakeTopicAPI) HasSubscriptions(_ context.Context, topicArn string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscriptionChecks++
	if f.observed.TopicArn != topicArn {
		return false, &mockAPIError{code: "NotFound", message: "missing topic"}
	}
	return f.hasSubscriptions, nil
}

func (f *fakeTopicAPI) UpdateTags(_ context.Context, topicArn string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.TopicArn != topicArn {
		return &mockAPIError{code: "NotFound", message: "missing topic"}
	}
	if f.failTagOnce {
		f.failTagOnce = false
		return &mockAPIError{code: "InvalidParameter", message: "injected tag fault after create"}
	}
	copyTags := maps.Clone(tags)
	f.tagCalls = append(f.tagCalls, copyTags)
	f.observed.Tags = maps.Clone(copyTags)
	return nil
}

func (f *fakeTopicAPI) FindByName(_ context.Context, topicName string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findCalls++
	if f.observed.TopicName == topicName {
		return f.observed.TopicArn, nil
	}
	return "", nil
}

func (f *fakeTopicAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{
		Creates: f.createCalls,
		Reads:   f.getCalls + f.findCalls + f.subscriptionChecks,
		Updates: len(f.attributeCalls) + len(f.tagCalls),
		Deletes: f.deleteCalls,
	}
}

func cloneTopicObserved(observed ObservedState) ObservedState {
	clone := observed
	clone.Tags = maps.Clone(observed.Tags)
	return clone
}

func (f *fakeTopicAPI) resetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attributeCalls = nil
	f.tagCalls = nil
}

func setupTopicDriver(t *testing.T, api TopicAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericSNSTopicDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) TopicAPI {
		return api
	})
	env := restatetest.Start(t, restate.Reflect(driver), restate.Reflect(topicDriftSink{}))
	return env.Ingress()
}

func provisionTopic(t *testing.T, client *ingress.Client, key string, spec SNSTopicSpec) SNSTopicOutputs {
	t.Helper()
	outputs, err := ingress.Object[types.ProvisionRequest, SNSTopicOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	return outputs
}

func TestGenericSNSTopicCoreLifecycleConformance(t *testing.T) {
	api := &fakeTopicAPI{}
	client := setupTopicDriver(t, api)
	key := "us-east-1~conformance-topic"
	spec := SNSTopicSpec{
		Account:   "test",
		Region:    "us-east-1",
		TopicName: "conformance-topic",
		Tags:      map[string]string{"suite": "core-lifecycle"},
	}

	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[SNSTopicSpec, SNSTopicOutputs]{
		Client:      client,
		ServiceName: ServiceName,
		Key:         key,
		Spec:        spec,
		Snapshot:    api.snapshot,
		AssertInputs: func(t *testing.T, inputs SNSTopicSpec) {
			assert.Equal(t, key, inputs.ManagedKey)
			assert.Equal(t, spec.TopicName, inputs.TopicName)
		},
	})
}

func TestGenericSNSTopicObservedImportConformance(t *testing.T) {
	api := &fakeTopicAPI{observed: ObservedState{
		TopicArn:  "arn:aws:sns:us-east-1:123456789012:observed-topic",
		TopicName: "observed-topic",
		Owner:     "123456789012",
		Tags:      map[string]string{"suite": "observed-import"},
	}}
	client := setupTopicDriver(t, api)

	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[SNSTopicOutputs]{
		Client:      client,
		ServiceName: ServiceName,
		Key:         "us-east-1~observed-topic",
		Ref:         types.ImportRef{ResourceID: "observed-topic", Account: "test"},
		Snapshot:    api.snapshot,
	})
}

func TestProvisionExistingTopicRemovesOmittedOptionalConfiguration(t *testing.T) {
	key := "us-east-1~alerts.fifo"
	api := &fakeTopicAPI{observed: ObservedState{
		TopicArn:                  "arn:aws:sns:us-east-1:123456789012:alerts.fifo",
		TopicName:                 "alerts.fifo",
		Owner:                     "123456789012",
		FifoTopic:                 true,
		DisplayName:               "Old alerts",
		Policy:                    `{"Version":"2012-10-17","Statement":[]}`,
		DeliveryPolicy:            `{"http":{"defaultHealthyRetryPolicy":{"numRetries":3}}}`,
		KmsMasterKeyId:            "alias/old",
		ContentBasedDeduplication: true,
		Tags:                      map[string]string{"env": "old"},
	}}
	client := setupTopicDriver(t, api)
	desired := SNSTopicSpec{
		Account:   "test",
		Region:    "us-east-1",
		TopicName: "alerts.fifo",
		FifoTopic: true,
		Tags:      map[string]string{},
	}
	provisionTopic(t, client, key, desired)

	api.mu.Lock()
	defer api.mu.Unlock()
	values := make(map[string]string, len(api.attributeCalls))
	for _, call := range api.attributeCalls {
		values[call.name] = call.value
	}
	assert.Equal(t, "", values["DisplayName"])
	assert.Equal(t, "{}", values["DeliveryPolicy"])
	assert.Equal(t, "", values["KmsMasterKeyId"])
	assert.Equal(t, "false", values["ContentBasedDeduplication"])
	assert.True(t, isDefaultTopicPolicy(values["Policy"], api.observed.TopicArn, api.observed.Owner))
	require.Len(t, api.tagCalls, 1)
	assert.Equal(t, map[string]string{"praxis:managed-key": key}, api.tagCalls[0])
	assert.False(t, HasDrift(desired, api.observed))
}

func TestProvisionExistingTopicSkipsSemanticallyEqualJSON(t *testing.T) {
	api := &fakeTopicAPI{}
	client := setupTopicDriver(t, api)
	key := "us-east-1~alerts"
	base := SNSTopicSpec{
		Account:        "test",
		Region:         "us-east-1",
		TopicName:      "alerts",
		Policy:         `{"Version":"2012-10-17","Statement":[]}`,
		DeliveryPolicy: `{"http":{"defaultHealthyRetryPolicy":{"numRetries":3}}}`,
		Tags:           map[string]string{"env": "prod"},
	}
	provisionTopic(t, client, key, base)
	api.resetCalls()
	base.Policy = `{ "Statement": [], "Version": "2012-10-17" }`
	base.DeliveryPolicy = `{"http": {"defaultHealthyRetryPolicy": {"numRetries": 3}}}`
	provisionTopic(t, client, key, base)

	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Empty(t, api.attributeCalls)
	assert.Empty(t, api.tagCalls)
}

func TestDeleteTopicKeepsTombstoneAndReconcileCannotResurrect(t *testing.T) {
	api := &fakeTopicAPI{}
	client := setupTopicDriver(t, api)
	key := "us-east-1~alerts"
	provisionTopic(t, client, key, SNSTopicSpec{Account: "test", Region: "us-east-1", TopicName: "alerts", Tags: map[string]string{}})

	_, err := ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusDeleted, status.Status)
	outputs, err := ingress.Object[restate.Void, SNSTopicOutputs](client, ServiceName, key, "GetOutputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Empty(t, outputs.TopicArn)

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

func TestGenericSNSTopicFindsAndConvergesExistingTopicBeforeCreate(t *testing.T) {
	api := &fakeTopicAPI{observed: ObservedState{
		TopicArn:    "arn:aws:sns:us-east-1:123456789012:existing",
		TopicName:   "existing",
		DisplayName: "old",
		Owner:       "123456789012",
		Tags:        map[string]string{"env": "old"},
	}}
	client := setupTopicDriver(t, api)
	key := "us-east-1~existing"
	spec := SNSTopicSpec{
		Account: "test", Region: "us-east-1", TopicName: "existing",
		DisplayName: "current", Tags: map[string]string{"env": "prod"},
	}

	outputs := provisionTopic(t, client, key, spec)
	assert.Equal(t, api.observed.TopicArn, outputs.TopicArn)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Zero(t, api.createCalls, "name lookup must recover the provider identity before Create")
	assert.Equal(t, "current", api.observed.DisplayName)
	assert.Equal(t, map[string]string{"env": "prod", "praxis:managed-key": key}, api.observed.Tags)
}

func TestGenericSNSTopicCreateResponseLossIsIdempotentByName(t *testing.T) {
	api := &fakeTopicAPI{createResponseLost: true}
	client := setupTopicDriver(t, api)
	key := "us-east-1~response-loss"

	outputs := provisionTopic(t, client, key, SNSTopicSpec{
		Account: "test", Region: "us-east-1", TopicName: "response-loss",
	})
	assert.Equal(t, "arn:aws:sns:us-east-1:123456789012:response-loss", outputs.TopicArn)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Equal(t, 2, api.createCalls, "the durable callback retries the idempotent CreateTopic request")
	assert.Equal(t, "response-loss", api.observed.TopicName, "retry must keep one provider topic identity")
}

func TestGenericSNSTopicRecoversPartialCreateOnNextProvision(t *testing.T) {
	api := &fakeTopicAPI{failTagOnce: true}
	client := setupTopicDriver(t, api)
	key := "us-east-1~partial"
	spec := SNSTopicSpec{
		Account: "test", Region: "us-east-1", TopicName: "partial",
		Tags: map[string]string{"env": "prod"},
	}

	_, err := ingress.Object[types.ProvisionRequest, SNSTopicOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	api.mu.Lock()
	assert.Equal(t, 1, api.createCalls)
	assert.Equal(t, "partial", api.observed.TopicName, "topic creation survives a later composite-step failure")
	api.mu.Unlock()

	outputs := provisionTopic(t, client, key, spec)
	assert.NotEmpty(t, outputs.TopicArn)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Equal(t, 1, api.createCalls, "recovery must find the existing topic instead of creating again")
	assert.Equal(t, map[string]string{"env": "prod", "praxis:managed-key": key}, api.observed.Tags)
}

func TestGenericSNSTopicRejectsImmutableFIFOChange(t *testing.T) {
	api := &fakeTopicAPI{}
	client := setupTopicDriver(t, api)
	key := "us-east-1~immutable.fifo"
	spec := SNSTopicSpec{
		Account: "test", Region: "us-east-1", TopicName: "immutable.fifo", FifoTopic: true,
	}
	provisionTopic(t, client, key, spec)

	spec.FifoTopic = false
	_, err := ingress.Object[types.ProvisionRequest, SNSTopicOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fifoTopic is immutable")
}

func TestGenericSNSTopicDeleteRejectsSeparateSubscriptions(t *testing.T) {
	api := &fakeTopicAPI{}
	client := setupTopicDriver(t, api)
	key := "us-east-1~subscribed"
	provisionTopic(t, client, key, SNSTopicSpec{Account: "test", Region: "us-east-1", TopicName: "subscribed"})
	api.mu.Lock()
	api.hasSubscriptions = true
	api.mu.Unlock()

	_, err := ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete the separate SNSSubscription resources first")
	api.mu.Lock()
	assert.True(t, api.hasSubscriptions)
	assert.Zero(t, api.deleteCalls, "the topic delete call must not cascade-delete separate subscriptions")
	api.hasSubscriptions = false
	api.mu.Unlock()

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
}

func TestGenericSNSTopicReconcileReportsExternalDeleteWithoutRecreation(t *testing.T) {
	api := &fakeTopicAPI{}
	client := setupTopicDriver(t, api)
	key := "us-east-1~externally-deleted"
	provisionTopic(t, client, key, SNSTopicSpec{Account: "test", Region: "us-east-1", TopicName: "externally-deleted"})
	api.mu.Lock()
	createsBefore := api.createCalls
	api.observed = ObservedState{}
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "SNSTopic resource was deleted externally")
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Equal(t, createsBefore, api.createCalls, "Reconcile reports deletion; only Provision may create")
}

func TestGenericSNSTopicObservedImportDoesNotAddManagedTags(t *testing.T) {
	api := &fakeTopicAPI{observed: ObservedState{
		TopicArn: "arn:aws:sns:us-east-1:123456789012:read-only", TopicName: "read-only",
		Owner: "123456789012", Tags: map[string]string{"external": "true"},
	}}
	client := setupTopicDriver(t, api)
	key := "us-east-1~read-only"

	_, err := ingress.Object[types.ImportRef, SNSTopicOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{
		ResourceID: "read-only", Account: "test",
	})
	require.NoError(t, err)
	_, err = ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Empty(t, api.tagCalls)
	assert.Equal(t, map[string]string{"external": "true"}, api.observed.Tags)
}
