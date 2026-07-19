package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strconv"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	"github.com/shirvan/praxis/internal/eventing"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type statefulQueueAPI struct {
	mu sync.Mutex

	exists   bool
	observed ObservedState

	creates int
	reads   int
	updates int
	deletes int

	createErrors            []error
	attributeErrors         []error
	setAttributeErrors      []error
	deleteErrors            []error
	partialTagTransientOnce bool
}

type queueDriftSink struct{}

func (queueDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }

func (queueDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

func (f *statefulQueueAPI) CreateQueue(_ context.Context, spec SQSQueueSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if len(f.createErrors) > 0 {
		err := f.createErrors[0]
		f.createErrors = f.createErrors[1:]
		return "", err
	}
	if f.exists {
		if f.observed.QueueName == spec.QueueName {
			return f.observed.QueueUrl, nil
		}
		return "", errors.New("QueueNameExists: queue exists")
	}
	f.exists = true
	f.observed = observedFromQueueSpec(spec)
	return f.observed.QueueUrl, nil
}

func (f *statefulQueueAPI) GetQueueUrl(_ context.Context, queueName string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if !f.exists || f.observed.QueueName != queueName {
		return "", errors.New("QueueDoesNotExist: missing")
	}
	return f.observed.QueueUrl, nil
}

func (f *statefulQueueAPI) GetQueueAttributes(_ context.Context, queueURL string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if len(f.attributeErrors) > 0 {
		err := f.attributeErrors[0]
		f.attributeErrors = f.attributeErrors[1:]
		return ObservedState{}, err
	}
	if !f.exists || f.observed.QueueUrl != queueURL {
		return ObservedState{}, errors.New("AWS.SimpleQueueService.NonExistentQueue: missing")
	}
	return cloneQueueObserved(f.observed), nil
}

func (f *statefulQueueAPI) SetQueueAttributes(_ context.Context, queueURL string, attrs map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	if len(f.setAttributeErrors) > 0 {
		err := f.setAttributeErrors[0]
		f.setAttributeErrors = f.setAttributeErrors[1:]
		return err
	}
	if !f.exists || f.observed.QueueUrl != queueURL {
		return errors.New("QueueDoesNotExist: missing")
	}
	applyQueueAttributes(&f.observed, attrs)
	return nil
}

func (f *statefulQueueAPI) DeleteQueue(_ context.Context, queueURL string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	if len(f.deleteErrors) > 0 {
		err := f.deleteErrors[0]
		f.deleteErrors = f.deleteErrors[1:]
		return err
	}
	if !f.exists || f.observed.QueueUrl != queueURL {
		return errors.New("QueueDoesNotExist: missing")
	}
	f.exists = false
	f.observed = ObservedState{}
	return nil
}

func (f *statefulQueueAPI) UpdateTags(_ context.Context, queueURL string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	if !f.exists || f.observed.QueueUrl != queueURL {
		return errors.New("QueueDoesNotExist: missing")
	}
	for key := range f.observed.Tags {
		if key != "praxis:managed-key" {
			delete(f.observed.Tags, key)
		}
	}
	if f.partialTagTransientOnce {
		f.partialTagTransientOnce = false
		return &smithy.GenericAPIError{Code: "ServiceUnavailable", Message: "tag write interrupted after removals"}
	}
	maps.Copy(f.observed.Tags, tags)
	return nil
}

func (f *statefulQueueAPI) GetTags(_ context.Context, queueURL string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.QueueUrl != queueURL {
		return nil, errors.New("QueueDoesNotExist: missing")
	}
	return maps.Clone(f.observed.Tags), nil
}

func (f *statefulQueueAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulQueueAPI) current() ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneQueueObserved(f.observed)
}

func (f *statefulQueueAPI) forceDrift() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed.VisibilityTimeout = 5
	f.observed.MessageRetentionPeriod = 120
	f.observed.DelaySeconds = 10
	f.observed.RedrivePolicy = nil
	f.observed.Tags = map[string]string{"env": "dev", "obsolete": "yes", "praxis:managed-key": managedQueueKey}
}

func setupGenericQueue(t *testing.T, api QueueAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericSQSQueueDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) QueueAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(queueDriftSink{})).Ingress()
}

const managedQueueKey = "us-east-1~generic-orders"

func managedQueueSpec() SQSQueueSpec {
	return SQSQueueSpec{
		Account: "test", Region: "us-east-1", QueueName: "generic-orders",
		VisibilityTimeout: 45, MessageRetentionPeriod: 86400, MaximumMessageSize: 131072,
		DelaySeconds: 2, ReceiveMessageWaitTimeSeconds: 10,
		RedrivePolicy:        &RedrivePolicy{DeadLetterTargetArn: "arn:aws:sqs:us-east-1:123456789012:orders-dlq", MaxReceiveCount: 5},
		SqsManagedSseEnabled: true, Tags: map[string]string{"env": "prod", "team": "platform"},
	}
}

func TestGenericSQSQueueCoreLifecycle(t *testing.T) {
	api := &statefulQueueAPI{}
	client := setupGenericQueue(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[SQSQueueSpec, SQSQueueOutputs]{
		Client: client, ServiceName: ServiceName, Key: managedQueueKey,
		Spec: managedQueueSpec(), Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs SQSQueueSpec) {
			assert.Equal(t, managedQueueKey, inputs.ManagedKey)
			assert.Equal(t, managedQueueSpec().RedrivePolicy, inputs.RedrivePolicy)
		},
	})
}

func TestGenericSQSQueueObservedImportLifecycle(t *testing.T) {
	spec := managedQueueSpec()
	spec.QueueName = "existing-orders"
	spec.ManagedKey = ""
	api := &statefulQueueAPI{exists: true, observed: observedFromQueueSpec(spec)}
	client := setupGenericQueue(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[SQSQueueOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~existing-orders",
		Ref: types.ImportRef{ResourceID: api.current().QueueArn, Account: "test"}, Snapshot: api.snapshot,
	})
	assert.NotContains(t, api.current().Tags, "praxis:managed-key", "Observed import must not claim the queue")
}

func TestGenericSQSQueueRecoversCreatedQueueAfterPostCreateFailure(t *testing.T) {
	api := &statefulQueueAPI{attributeErrors: []error{errors.New("AccessDenied: temporary test permission boundary")}}
	client := setupGenericQueue(t, api)
	spec := managedQueueSpec()

	_, err := ingress.Object[SQSQueueSpec, SQSQueueOutputs](client, ServiceName, managedQueueKey, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.Equal(t, "generic-orders", api.current().QueueName)
	assert.Equal(t, managedQueueKey, api.current().Tags["praxis:managed-key"])

	outputs, err := ingress.Object[SQSQueueSpec, SQSQueueOutputs](client, ServiceName, managedQueueKey, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, "generic-orders", outputs.QueueName)
	assert.Equal(t, 1, api.snapshot().Creates, "managed-key recovery must not issue a second CreateQueue")
}

func TestGenericSQSQueueRejectsUnownedSameNameQueue(t *testing.T) {
	spec := managedQueueSpec()
	external := observedFromQueueSpec(spec)
	external.Tags = map[string]string{"owner": "external"}
	api := &statefulQueueAPI{exists: true, observed: external}
	client := setupGenericQueue(t, api)

	_, err := ingress.Object[SQSQueueSpec, SQSQueueOutputs](client, ServiceName, managedQueueKey, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not owned")
	assert.Zero(t, api.snapshot().Creates)
	assert.Zero(t, api.snapshot().Updates)
	assert.Equal(t, map[string]string{"owner": "external"}, api.current().Tags)

	_, deleteErr := ingress.Object[restate.Void, restate.Void](client, ServiceName, managedQueueKey, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, deleteErr)
	assert.Contains(t, deleteErr.Error(), "refusing to delete")
	assert.Zero(t, api.snapshot().Deletes, "a failed Provision must not let Delete remove the unowned name match")
	assert.Equal(t, "generic-orders", api.current().QueueName)
}

func TestGenericSQSQueueIgnoresMissingInternalTagButRejectsDifferentOwner(t *testing.T) {
	api := &statefulQueueAPI{}
	client := setupGenericQueue(t, api)
	spec := managedQueueSpec()
	_, err := ingress.Object[SQSQueueSpec, SQSQueueOutputs](client, ServiceName, managedQueueKey, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	api.mu.Lock()
	delete(api.observed.Tags, "praxis:managed-key")
	api.mu.Unlock()
	beforeMissing := api.snapshot()
	_, err = ingress.Object[SQSQueueSpec, SQSQueueOutputs](client, ServiceName, managedQueueKey, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.NotContains(t, api.current().Tags, "praxis:managed-key", "the internal recovery tag is not user-declarative drift")
	assert.Equal(t, beforeMissing.Updates, api.snapshot().Updates)

	api.mu.Lock()
	api.observed.Tags["praxis:managed-key"] = "us-east-1~different-owner"
	api.mu.Unlock()
	beforeConflict := api.snapshot()
	_, err = ingress.Object[SQSQueueSpec, SQSQueueOutputs](client, ServiceName, managedQueueKey, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "different-owner")
	assert.Equal(t, beforeConflict.Updates, api.snapshot().Updates, "ownership conflicts must be rejected, not overwritten")
}

func TestGenericSQSQueueRetriesTransientCreateInsideDurableCallback(t *testing.T) {
	api := &statefulQueueAPI{createErrors: []error{&smithy.GenericAPIError{
		Code: "QueueDeletedRecently", Message: "same-name recreation cooldown",
	}}}
	client := setupGenericQueue(t, api)

	outputs, err := ingress.Object[SQSQueueSpec, SQSQueueOutputs](client, ServiceName, managedQueueKey, "Provision").Request(t.Context(), managedQueueSpec())
	require.NoError(t, err)
	assert.Equal(t, "generic-orders", outputs.QueueName)
	assert.Equal(t, 2, api.snapshot().Creates, "temporary SQS cooldown must retry in the journaled callback")
}

func TestGenericSQSQueueNameConflictIsTerminal(t *testing.T) {
	api := &statefulQueueAPI{createErrors: []error{errors.New("QueueNameExists: different attributes")}}
	client := setupGenericQueue(t, api)

	_, err := ingress.Object[SQSQueueSpec, SQSQueueOutputs](client, ServiceName, managedQueueKey, "Provision").Request(t.Context(), managedQueueSpec())
	require.Error(t, err)
	assert.Equal(t, 1, api.snapshot().Creates, "permanent name conflicts must not retry")
}

func TestGenericSQSQueueManagedReconcileCorrectsAttributesRedriveAndTags(t *testing.T) {
	api := &statefulQueueAPI{}
	client := setupGenericQueue(t, api)
	spec := managedQueueSpec()
	_, err := ingress.Object[SQSQueueSpec, SQSQueueOutputs](client, ServiceName, managedQueueKey, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	api.forceDrift()
	before := api.snapshot()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, managedQueueKey, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.GreaterOrEqual(t, api.snapshot().Updates-before.Updates, 2, "attributes and tags must converge independently")
	assertQueueMatchesSpec(t, spec, api.current())
}

func TestGenericSQSQueueTagMutationRecoversAfterPartialCompletion(t *testing.T) {
	api := &statefulQueueAPI{}
	client := setupGenericQueue(t, api)
	spec := managedQueueSpec()
	_, err := ingress.Object[SQSQueueSpec, SQSQueueOutputs](client, ServiceName, managedQueueKey, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	api.mu.Lock()
	api.observed.Tags = map[string]string{"obsolete": "remove", "praxis:managed-key": managedQueueKey}
	api.partialTagTransientOnce = true
	api.mu.Unlock()
	before := api.snapshot()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, managedQueueKey, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Equal(t, 2, api.snapshot().Updates-before.Updates, "the interrupted tag callback must retry and finish convergence")
	assert.Equal(t, managedTags(spec.Tags, managedQueueKey), api.current().Tags)
}

func TestGenericSQSQueueExternalDeleteRequiresReplacementWithoutCreate(t *testing.T) {
	api := &statefulQueueAPI{}
	client := setupGenericQueue(t, api)
	_, err := ingress.Object[SQSQueueSpec, SQSQueueOutputs](client, ServiceName, managedQueueKey, "Provision").Request(t.Context(), managedQueueSpec())
	require.NoError(t, err)
	api.mu.Lock()
	api.exists = false
	api.observed = ObservedState{}
	api.mu.Unlock()
	before := api.snapshot()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, managedQueueKey, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "deleted externally")
	assert.Equal(t, before.Creates, api.snapshot().Creates, "Reconcile must not recreate an externally deleted queue")
}

func TestGenericSQSQueueDeleteTreatsProviderNotFoundAsSuccess(t *testing.T) {
	api := &statefulQueueAPI{}
	client := setupGenericQueue(t, api)
	_, err := ingress.Object[SQSQueueSpec, SQSQueueOutputs](client, ServiceName, managedQueueKey, "Provision").Request(t.Context(), managedQueueSpec())
	require.NoError(t, err)
	api.mu.Lock()
	api.exists = false
	api.deleteErrors = []error{errors.New("QueueDoesNotExist: already gone")}
	api.mu.Unlock()

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, managedQueueKey, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, managedQueueKey, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusDeleted, status.Status)
}

func TestGenericSQSQueueLateInitializesFIFOProviderDefaults(t *testing.T) {
	spec := SQSQueueSpec{
		Account: "test", Region: "us-east-1", QueueName: "events.fifo", FifoQueue: true,
		VisibilityTimeout: 30, MessageRetentionPeriod: 345600, MaximumMessageSize: 262144,
		SqsManagedSseEnabled: true, ManagedKey: "us-east-1~events.fifo",
	}
	api := &statefulQueueAPI{}
	client := setupGenericQueue(t, api)
	key := "us-east-1~events.fifo"
	_, err := ingress.Object[SQSQueueSpec, SQSQueueOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	inputs, err := ingress.Object[restate.Void, SQSQueueSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, "queue", inputs.DeduplicationScope)
	assert.Equal(t, "perQueue", inputs.FifoThroughputLimit)
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Condition(t, func() bool {
		for _, condition := range status.Conditions {
			if condition.Type == types.ConditionInitialized && condition.Status == types.ConditionTrue {
				return true
			}
		}
		return false
	})
}

func TestValidateImmutableQueueIdentity(t *testing.T) {
	observed := ObservedState{
		QueueName: "orders", QueueArn: "arn:aws:sqs:us-east-1:123456789012:orders", FifoQueue: false,
	}
	assert.ErrorContains(t, validateImmutableIdentity(SQSQueueSpec{Region: "us-east-1", QueueName: "other"}, observed), "queueName")
	assert.ErrorContains(t, validateImmutableIdentity(SQSQueueSpec{Region: "us-east-1", QueueName: "orders", FifoQueue: true}, observed), "fifoQueue")
	assert.ErrorContains(t, validateImmutableIdentity(SQSQueueSpec{Region: "us-west-2", QueueName: "orders"}, observed), "region")
}

func TestChangedQueueAttributesDoesNotOwnSeparateQueuePolicy(t *testing.T) {
	desired := managedQueueSpec()
	observed := observedFromQueueSpec(desired)
	desired.VisibilityTimeout = 75
	attrs, err := changedQueueAttributes(desired, observed)
	require.NoError(t, err)
	assert.Equal(t, "75", attrs["VisibilityTimeout"])
	assert.NotContains(t, attrs, "Policy", "SQSQueuePolicy has a separate driver and must not be absorbed")
}

func observedFromQueueSpec(spec SQSQueueSpec) ObservedState {
	name := spec.QueueName
	url := "https://sqs.us-east-1.amazonaws.com/123456789012/" + name
	dedupScope := spec.DeduplicationScope
	throughput := spec.FifoThroughputLimit
	if spec.FifoQueue {
		if dedupScope == "" {
			dedupScope = "queue"
		}
		if throughput == "" {
			throughput = "perQueue"
		}
	}
	return ObservedState{
		QueueUrl: url, QueueArn: "arn:aws:sqs:us-east-1:123456789012:" + name, QueueName: name,
		FifoQueue: spec.FifoQueue, VisibilityTimeout: spec.VisibilityTimeout,
		MessageRetentionPeriod: spec.MessageRetentionPeriod, MaximumMessageSize: spec.MaximumMessageSize,
		DelaySeconds: spec.DelaySeconds, ReceiveMessageWaitTimeSeconds: spec.ReceiveMessageWaitTimeSeconds,
		RedrivePolicy: cloneRedrivePolicy(spec.RedrivePolicy), SqsManagedSseEnabled: spec.SqsManagedSseEnabled,
		KmsMasterKeyId: spec.KmsMasterKeyId, KmsDataKeyReusePeriodSeconds: spec.KmsDataKeyReusePeriodSeconds,
		ContentBasedDeduplication: spec.ContentBasedDeduplication,
		DeduplicationScope:        dedupScope, FifoThroughputLimit: throughput,
		Tags: managedTags(spec.Tags, spec.ManagedKey), CreatedTimestamp: "1700000000", LastModifiedTimestamp: "1700000000",
	}
}

func applyQueueAttributes(observed *ObservedState, attrs map[string]string) {
	for name, value := range attrs {
		switch name {
		case "VisibilityTimeout":
			observed.VisibilityTimeout, _ = strconv.Atoi(value)
		case "MessageRetentionPeriod":
			observed.MessageRetentionPeriod, _ = strconv.Atoi(value)
		case "MaximumMessageSize":
			observed.MaximumMessageSize, _ = strconv.Atoi(value)
		case "DelaySeconds":
			observed.DelaySeconds, _ = strconv.Atoi(value)
		case "ReceiveMessageWaitTimeSeconds":
			observed.ReceiveMessageWaitTimeSeconds, _ = strconv.Atoi(value)
		case "RedrivePolicy":
			observed.RedrivePolicy = nil
			if value != "" {
				var policy RedrivePolicy
				_ = json.Unmarshal([]byte(value), &policy)
				observed.RedrivePolicy = &policy
			}
		case "KmsMasterKeyId":
			observed.KmsMasterKeyId = value
		case "KmsDataKeyReusePeriodSeconds":
			observed.KmsDataKeyReusePeriodSeconds, _ = strconv.Atoi(value)
		case "SqsManagedSseEnabled":
			observed.SqsManagedSseEnabled, _ = strconv.ParseBool(value)
		case "ContentBasedDeduplication":
			observed.ContentBasedDeduplication, _ = strconv.ParseBool(value)
		case "DeduplicationScope":
			observed.DeduplicationScope = value
		case "FifoThroughputLimit":
			observed.FifoThroughputLimit = value
		}
	}
}

func cloneQueueObserved(input ObservedState) ObservedState {
	output := input
	output.RedrivePolicy = cloneRedrivePolicy(input.RedrivePolicy)
	output.Tags = maps.Clone(input.Tags)
	return output
}

func assertQueueMatchesSpec(t *testing.T, spec SQSQueueSpec, observed ObservedState) {
	t.Helper()
	assert.Equal(t, spec.QueueName, observed.QueueName)
	assert.Equal(t, spec.FifoQueue, observed.FifoQueue)
	assert.Equal(t, spec.VisibilityTimeout, observed.VisibilityTimeout)
	assert.Equal(t, spec.MessageRetentionPeriod, observed.MessageRetentionPeriod)
	assert.Equal(t, spec.MaximumMessageSize, observed.MaximumMessageSize)
	assert.Equal(t, spec.DelaySeconds, observed.DelaySeconds)
	assert.Equal(t, spec.ReceiveMessageWaitTimeSeconds, observed.ReceiveMessageWaitTimeSeconds)
	assert.Equal(t, spec.RedrivePolicy, observed.RedrivePolicy)
	assert.Equal(t, spec.SqsManagedSseEnabled, observed.SqsManagedSseEnabled)
	assert.Equal(t, spec.KmsMasterKeyId, observed.KmsMasterKeyId)
	assert.Equal(t, drivers.FilterPraxisTags(spec.Tags), drivers.FilterPraxisTags(observed.Tags))
}
