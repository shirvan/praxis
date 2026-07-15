package snstopic

import (
	"context"
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

	observed       ObservedState
	attributeCalls []topicAttributeCall
	tagCalls       []map[string]string
	createCalls    int
	getCalls       int
	deleteCalls    int
}

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

func (f *fakeTopicAPI) UpdateTags(_ context.Context, topicArn string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.TopicArn != topicArn {
		return &mockAPIError{code: "NotFound", message: "missing topic"}
	}
	copyTags := maps.Clone(tags)
	f.tagCalls = append(f.tagCalls, copyTags)
	f.observed.Tags = maps.Clone(copyTags)
	return nil
}

func (f *fakeTopicAPI) FindByName(_ context.Context, topicName string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.TopicName == topicName {
		return f.observed.TopicArn, nil
	}
	return "", nil
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
	driver := NewSNSTopicDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) TopicAPI {
		return api
	})
	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress()
}

func provisionTopic(t *testing.T, client *ingress.Client, key string, spec SNSTopicSpec) SNSTopicOutputs {
	t.Helper()
	outputs, err := ingress.Object[SNSTopicSpec, SNSTopicOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	return outputs
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
