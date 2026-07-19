package sqspolicy

import (
	"context"
	"errors"
	"maps"
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
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	"github.com/shirvan/praxis/internal/eventing"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type statefulPolicyAPI struct {
	mu sync.Mutex

	queueExists bool
	queueName   string
	queueURL    string
	queueARN    string
	policy      string
	otherAttrs  map[string]string

	reads       int
	setCalls    int
	removeCalls int
	creates     int
	updates     int
	deletes     int

	getErrors           []error
	setErrors           []error
	setAfterApplyErrors []error
	removeErrors        []error
}

type policyDriftSink struct{}

func (policyDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }

func (policyDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

func newStatefulPolicyAPI(name, policy string) *statefulPolicyAPI {
	return &statefulPolicyAPI{
		queueExists: true, queueName: name,
		queueURL: "https://sqs.us-east-1.amazonaws.com/123456789012/" + name,
		queueARN: "arn:aws:sqs:us-east-1:123456789012:" + name,
		policy:   policy, otherAttrs: map[string]string{"VisibilityTimeout": "45"},
	}
}

func (f *statefulPolicyAPI) GetQueueUrl(_ context.Context, queueName string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if !f.queueExists || f.queueName != queueName {
		return "", errors.New("QueueDoesNotExist: missing")
	}
	return f.queueURL, nil
}

func (f *statefulPolicyAPI) GetQueuePolicy(_ context.Context, queueURL string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if len(f.getErrors) > 0 {
		err := f.getErrors[0]
		f.getErrors = f.getErrors[1:]
		return ObservedState{}, err
	}
	if !f.queueExists || f.queueURL != queueURL {
		return ObservedState{}, errors.New("AWS.SimpleQueueService.NonExistentQueue: missing")
	}
	return ObservedState{QueueUrl: f.queueURL, QueueArn: f.queueARN, Policy: f.policy}, nil
}

func (f *statefulPolicyAPI) SetQueuePolicy(_ context.Context, queueURL, policy string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls++
	if len(f.setErrors) > 0 {
		err := f.setErrors[0]
		f.setErrors = f.setErrors[1:]
		return err
	}
	if !f.queueExists || f.queueURL != queueURL {
		return errors.New("QueueDoesNotExist: missing")
	}
	if f.policy == "" {
		f.creates++
	} else if !policiesEqual(f.policy, policy) {
		f.updates++
	}
	f.policy = policy
	if len(f.setAfterApplyErrors) > 0 {
		err := f.setAfterApplyErrors[0]
		f.setAfterApplyErrors = f.setAfterApplyErrors[1:]
		return err
	}
	return nil
}

func (f *statefulPolicyAPI) RemoveQueuePolicy(_ context.Context, queueURL string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls++
	if len(f.removeErrors) > 0 {
		err := f.removeErrors[0]
		f.removeErrors = f.removeErrors[1:]
		return err
	}
	if !f.queueExists || f.queueURL != queueURL {
		return errors.New("QueueDoesNotExist: missing")
	}
	if f.policy != "" {
		f.deletes++
	}
	f.policy = ""
	return nil
}

func (f *statefulPolicyAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

type policySnapshot struct {
	queueExists bool
	policy      string
	otherAttrs  map[string]string
	setCalls    int
	removeCalls int
}

func (f *statefulPolicyAPI) current() policySnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return policySnapshot{
		queueExists: f.queueExists, policy: f.policy, otherAttrs: maps.Clone(f.otherAttrs),
		setCalls: f.setCalls, removeCalls: f.removeCalls,
	}
}

func setupGenericPolicy(t *testing.T, api PolicyAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericSQSQueuePolicyDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) PolicyAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(policyDriftSink{})).Ingress()
}

const (
	managedPolicyKey  = "us-east-1~generic-orders"
	managedPolicyJSON = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}`
)

func managedPolicySpec() SQSQueuePolicySpec {
	return SQSQueuePolicySpec{
		Account: "test", Region: "us-east-1", QueueName: "generic-orders", Policy: managedPolicyJSON,
	}
}

func TestGenericSQSQueuePolicyCoreLifecycle(t *testing.T) {
	api := newStatefulPolicyAPI("generic-orders", "")
	client := setupGenericPolicy(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[SQSQueuePolicySpec, SQSQueuePolicyOutputs]{
		Client: client, ServiceName: ServiceName, Key: managedPolicyKey,
		Spec: managedPolicySpec(), Snapshot: api.snapshot,
	})
	current := api.current()
	assert.True(t, current.queueExists, "deleting the policy must preserve its queue")
	assert.Equal(t, map[string]string{"VisibilityTimeout": "45"}, current.otherAttrs)
}

func TestGenericSQSQueuePolicyObservedImportLifecycle(t *testing.T) {
	api := newStatefulPolicyAPI("existing-orders", `{"Statement":[],"Version":"2012-10-17"}`)
	client := setupGenericPolicy(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[SQSQueuePolicyOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~existing-orders",
		Ref: types.ImportRef{ResourceID: api.queueARN, Account: "test"}, Snapshot: api.snapshot,
	})
	assert.Equal(t, `{"Statement":[],"Version":"2012-10-17"}`, api.current().policy)
}

func TestGenericSQSQueuePolicyFormattingOnlyDifferenceDoesNotWrite(t *testing.T) {
	api := newStatefulPolicyAPI("generic-orders", `{"Statement":[{"Resource":"*","Action":"sqs:SendMessage","Effect":"Allow"}],"Version":"2012-10-17"}`)
	client := setupGenericPolicy(t, api)
	before := api.current()

	outputs, err := ingress.Object[SQSQueuePolicySpec, SQSQueuePolicyOutputs](
		client, ServiceName, managedPolicyKey, "Provision",
	).Request(t.Context(), managedPolicySpec())
	require.NoError(t, err)
	assert.Equal(t, "generic-orders", outputs.QueueName)
	assert.Equal(t, before.setCalls, api.current().setCalls, "semantic JSON equality must avoid a provider write")
}

func TestGenericSQSQueuePolicyRecoversAfterSetCompletesBeforeFailure(t *testing.T) {
	api := newStatefulPolicyAPI("generic-orders", "")
	api.setAfterApplyErrors = []error{errors.New("AccessDenied: response lost after apply")}
	client := setupGenericPolicy(t, api)

	_, err := ingress.Object[SQSQueuePolicySpec, SQSQueuePolicyOutputs](client, ServiceName, managedPolicyKey, "Provision").Request(t.Context(), managedPolicySpec())
	require.Error(t, err)
	assert.Equal(t, managedPolicyJSON, api.current().policy)
	assert.Equal(t, 1, api.current().setCalls)

	_, err = ingress.Object[SQSQueuePolicySpec, SQSQueuePolicyOutputs](client, ServiceName, managedPolicyKey, "Provision").Request(t.Context(), managedPolicySpec())
	require.NoError(t, err)
	assert.Equal(t, 1, api.current().setCalls, "observe-before-create must recover the completed policy write")
}

func TestGenericSQSQueuePolicyRetriesTransientPartialSetIdempotently(t *testing.T) {
	api := newStatefulPolicyAPI("generic-orders", "")
	api.setAfterApplyErrors = []error{&smithy.GenericAPIError{Code: "ServiceUnavailable", Message: "response lost"}}
	client := setupGenericPolicy(t, api)

	_, err := ingress.Object[SQSQueuePolicySpec, SQSQueuePolicyOutputs](client, ServiceName, managedPolicyKey, "Provision").Request(t.Context(), managedPolicySpec())
	require.NoError(t, err)
	assert.Equal(t, 2, api.current().setCalls, "the journal callback must retry the idempotent Policy replacement")
	assert.Equal(t, 1, api.snapshot().Creates, "retry must not create a second policy subresource")
}

func TestGenericSQSQueuePolicyInvalidSetIsTerminal(t *testing.T) {
	api := newStatefulPolicyAPI("generic-orders", "")
	api.setErrors = []error{errors.New("InvalidAttributeValue: malformed policy")}
	client := setupGenericPolicy(t, api)

	_, err := ingress.Object[SQSQueuePolicySpec, SQSQueuePolicyOutputs](client, ServiceName, managedPolicyKey, "Provision").Request(t.Context(), managedPolicySpec())
	require.Error(t, err)
	assert.Equal(t, 1, api.current().setCalls, "permanent request errors must not retry")
}

func TestGenericSQSQueuePolicyManagedReconcileCorrectsOnlyPolicy(t *testing.T) {
	api := newStatefulPolicyAPI("generic-orders", "")
	client := setupGenericPolicy(t, api)
	_, err := ingress.Object[SQSQueuePolicySpec, SQSQueuePolicyOutputs](client, ServiceName, managedPolicyKey, "Provision").Request(t.Context(), managedPolicySpec())
	require.NoError(t, err)
	api.mu.Lock()
	api.policy = `{"Version":"2012-10-17","Statement":[]}`
	api.otherAttrs["VisibilityTimeout"] = "90"
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, managedPolicyKey, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	current := api.current()
	assert.True(t, policiesEqual(managedPolicyJSON, current.policy))
	assert.Equal(t, "90", current.otherAttrs["VisibilityTimeout"], "policy convergence must not touch queue attributes")
}

func TestGenericSQSQueuePolicyExternalRemovalRequiresReplacementWithoutWrite(t *testing.T) {
	api := newStatefulPolicyAPI("generic-orders", "")
	client := setupGenericPolicy(t, api)
	_, err := ingress.Object[SQSQueuePolicySpec, SQSQueuePolicyOutputs](client, ServiceName, managedPolicyKey, "Provision").Request(t.Context(), managedPolicySpec())
	require.NoError(t, err)
	api.mu.Lock()
	api.policy = ""
	api.mu.Unlock()
	before := api.current()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, managedPolicyKey, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "deleted externally")
	assert.Equal(t, before.setCalls, api.current().setCalls, "Reconcile must not recreate an externally removed policy")
}

func TestGenericSQSQueuePolicyMissingQueueIsNotCreatedOrDeleted(t *testing.T) {
	api := newStatefulPolicyAPI("generic-orders", "")
	api.queueExists = false
	client := setupGenericPolicy(t, api)

	_, err := ingress.Object[SQSQueuePolicySpec, SQSQueuePolicyOutputs](client, ServiceName, managedPolicyKey, "Provision").Request(t.Context(), managedPolicySpec())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
	assert.Zero(t, api.current().setCalls)
	assert.Zero(t, api.current().removeCalls)
}

func TestGenericSQSQueuePolicyImportRejectsQueueWithoutPolicy(t *testing.T) {
	api := newStatefulPolicyAPI("generic-orders", "")
	client := setupGenericPolicy(t, api)

	_, err := ingress.Object[types.ImportRef, SQSQueuePolicyOutputs](
		client, ServiceName, managedPolicyKey, "Import",
	).Request(t.Context(), types.ImportRef{ResourceID: "generic-orders", Account: "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
	assert.Zero(t, api.current().setCalls, "Import must not create a missing policy")
}

func TestGenericSQSQueuePolicyCannotRetargetCommittedPolicy(t *testing.T) {
	api := newStatefulPolicyAPI("generic-orders", "")
	client := setupGenericPolicy(t, api)
	_, err := ingress.Object[SQSQueuePolicySpec, SQSQueuePolicyOutputs](client, ServiceName, managedPolicyKey, "Provision").Request(t.Context(), managedPolicySpec())
	require.NoError(t, err)
	before := api.current()

	spec := managedPolicySpec()
	spec.QueueName = "different-orders"
	_, err = ingress.Object[SQSQueuePolicySpec, SQSQueuePolicyOutputs](client, ServiceName, managedPolicyKey, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "queueName is immutable")
	after := api.current()
	assert.Equal(t, before.setCalls, after.setCalls)
	assert.True(t, policiesEqual(before.policy, after.policy))
}

func TestGenericSQSQueuePolicyRejectsInvalidJSONBeforeProviderWrite(t *testing.T) {
	api := newStatefulPolicyAPI("generic-orders", "")
	client := setupGenericPolicy(t, api)
	spec := managedPolicySpec()
	spec.Policy = `[]`
	_, err := ingress.Object[SQSQueuePolicySpec, SQSQueuePolicyOutputs](client, ServiceName, managedPolicyKey, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JSON object")
	assert.Zero(t, api.current().setCalls)
}

func TestValidateImmutablePolicyIdentity(t *testing.T) {
	observed := ObservedState{
		QueueUrl: "https://sqs.us-east-1.amazonaws.com/123456789012/orders",
		QueueArn: "arn:aws:sqs:us-east-1:123456789012:orders", Policy: managedPolicyJSON,
	}
	assert.ErrorContains(t, validateImmutableIdentity(SQSQueuePolicySpec{Region: "us-east-1", QueueName: "other"}, observed), "queueName")
	assert.ErrorContains(t, validateImmutableIdentity(SQSQueuePolicySpec{Region: "us-west-2", QueueName: "orders"}, observed), "region")
}
