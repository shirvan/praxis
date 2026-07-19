package loggroup

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

type statefulLogGroupAPI struct {
	mu sync.Mutex

	observed ObservedState
	exists   bool

	creates         int
	reads           int
	retentionWrites int
	kmsWrites       int
	tagWrites       int
	deletes         int
	createErrors    []error
	deleteErrors    []error
}

type logGroupDriftSink struct{}

func (logGroupDriftSink) ServiceName() string {
	return eventing.ResourceEventBridgeServiceName
}

func (logGroupDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error {
	return nil
}

func (f *statefulLogGroupAPI) CreateLogGroup(_ context.Context, spec LogGroupSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if len(f.createErrors) > 0 {
		err := f.createErrors[0]
		f.createErrors = f.createErrors[1:]
		return err
	}
	f.exists = true
	f.observed = ObservedState{
		ARN:          "arn:aws:logs:us-east-1:123456789012:log-group:" + spec.LogGroupName,
		LogGroupName: spec.LogGroupName, LogGroupClass: spec.LogGroupClass,
		KmsKeyID: spec.KmsKeyID, CreationTime: 1700000000000,
		Tags: managedTags(spec.Tags, spec.ManagedKey),
	}
	return nil
}

func (f *statefulLogGroupAPI) DescribeLogGroup(_ context.Context, name string) (ObservedState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if !f.exists || f.observed.LogGroupName != name {
		return ObservedState{}, false, nil
	}
	return cloneObserved(f.observed), true, nil
}

func (f *statefulLogGroupAPI) PutRetentionPolicy(_ context.Context, _ string, retention int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retentionWrites++
	f.observed.RetentionInDays = aws.Int32(retention)
	return nil
}

func (f *statefulLogGroupAPI) DeleteRetentionPolicy(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retentionWrites++
	f.observed.RetentionInDays = nil
	return nil
}

func (f *statefulLogGroupAPI) AssociateKmsKey(_ context.Context, _, kmsKeyID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kmsWrites++
	f.observed.KmsKeyID = kmsKeyID
	return nil
}

func (f *statefulLogGroupAPI) DisassociateKmsKey(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kmsWrites++
	f.observed.KmsKeyID = ""
	return nil
}

func (f *statefulLogGroupAPI) DeleteLogGroup(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	if len(f.deleteErrors) > 0 {
		err := f.deleteErrors[0]
		f.deleteErrors = f.deleteErrors[1:]
		return err
	}
	f.exists = false
	f.observed = ObservedState{}
	return nil
}

func (f *statefulLogGroupAPI) TagResource(_ context.Context, _ string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tagWrites++
	if f.observed.Tags == nil {
		f.observed.Tags = map[string]string{}
	}
	maps.Copy(f.observed.Tags, tags)
	return nil
}

func (f *statefulLogGroupAPI) UntagResource(_ context.Context, _ string, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tagWrites++
	for _, key := range keys {
		delete(f.observed.Tags, key)
	}
	return nil
}

func (f *statefulLogGroupAPI) ListTagsForResource(context.Context, string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.observed.Tags), nil
}

func (f *statefulLogGroupAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{
		Creates: f.creates, Reads: f.reads,
		Updates: f.retentionWrites + f.kmsWrites + f.tagWrites,
		Deletes: f.deletes,
	}
}

type logGroupMutationSnapshot struct {
	observed        ObservedState
	retentionWrites int
	kmsWrites       int
	tagWrites       int
}

func (f *statefulLogGroupAPI) mutationSnapshot() logGroupMutationSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return logGroupMutationSnapshot{
		observed: cloneObserved(f.observed), retentionWrites: f.retentionWrites,
		kmsWrites: f.kmsWrites, tagWrites: f.tagWrites,
	}
}

func (f *statefulLogGroupAPI) setDrift(retention int32, kmsKeyID string, tags map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed.RetentionInDays = aws.Int32(retention)
	f.observed.KmsKeyID = kmsKeyID
	f.observed.Tags = maps.Clone(tags)
}

func cloneObserved(input ObservedState) ObservedState {
	output := input
	if input.RetentionInDays != nil {
		output.RetentionInDays = aws.Int32(*input.RetentionInDays)
	}
	output.Tags = maps.Clone(input.Tags)
	return output
}

func setupGenericLogGroup(t *testing.T, api LogGroupAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := NewGenericLogGroupDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) LogGroupAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(logGroupDriftSink{})).Ingress()
}

func managedLogGroupSpec() LogGroupSpec {
	retention := int32(14)
	return LogGroupSpec{
		Account: "test", Region: "us-east-1", LogGroupName: "/praxis/generic",
		LogGroupClass: "STANDARD", RetentionInDays: &retention, KmsKeyID: "kms-desired",
		Tags: map[string]string{"env": "prod", "team": "platform"},
	}
}

const managedLogGroupKey = "us-east-1~praxis-generic"

func TestGenericLogGroupCoreLifecycle(t *testing.T) {
	api := &statefulLogGroupAPI{}
	client := setupGenericLogGroup(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[LogGroupSpec, LogGroupOutputs]{
		Client: client, ServiceName: ServiceName, Key: managedLogGroupKey,
		Spec: managedLogGroupSpec(), Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs LogGroupSpec) {
			assert.Equal(t, managedLogGroupKey, inputs.ManagedKey)
			assert.Equal(t, managedLogGroupSpec().RetentionInDays, inputs.RetentionInDays)
		},
	})
}

func TestGenericLogGroupObservedImportLifecycle(t *testing.T) {
	retention := int32(30)
	api := &statefulLogGroupAPI{exists: true, observed: ObservedState{
		ARN:          "arn:aws:logs:us-east-1:123456789012:log-group:/praxis/existing",
		LogGroupName: "/praxis/existing", LogGroupClass: "STANDARD",
		RetentionInDays: &retention, KmsKeyID: "kms-existing",
		Tags: map[string]string{"env": "prod"},
	}}
	client := setupGenericLogGroup(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[LogGroupOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~praxis-existing",
		Ref: types.ImportRef{ResourceID: "/praxis/existing", Account: "test"}, Snapshot: api.snapshot,
	})
	assert.Zero(t, api.mutationSnapshot().tagWrites, "Observed import and reconcile must not write internal or user tags")
}

func TestGenericLogGroupReconcileCorrectsRetentionKMSAndTagsIndependently(t *testing.T) {
	api := &statefulLogGroupAPI{}
	client := setupGenericLogGroup(t, api)
	spec := managedLogGroupSpec()
	key := managedLogGroupKey

	_, err := ingress.Object[types.ProvisionRequest, LogGroupOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	api.setDrift(90, "kms-drifted", map[string]string{
		"env": "dev", "obsolete": "remove-me", "praxis:managed-key": key,
	})
	before := api.mutationSnapshot()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	after := api.mutationSnapshot()

	require.NotNil(t, after.observed.RetentionInDays)
	assert.Equal(t, int32(14), *after.observed.RetentionInDays)
	assert.Equal(t, "kms-desired", after.observed.KmsKeyID)
	assert.Equal(t, map[string]string{
		"env": "prod", "team": "platform", "praxis:managed-key": key,
	}, after.observed.Tags)
	assert.Greater(t, after.retentionWrites, before.retentionWrites)
	assert.Greater(t, after.kmsWrites, before.kmsWrites)
	assert.Greater(t, after.tagWrites, before.tagWrites)
}

func TestGenericLogGroupRejectsImmutableClassChange(t *testing.T) {
	api := &statefulLogGroupAPI{exists: true, observed: ObservedState{
		ARN:          "arn:aws:logs:us-east-1:123456789012:log-group:/praxis/generic",
		LogGroupName: "/praxis/generic", LogGroupClass: "INFREQUENT_ACCESS",
		Tags: map[string]string{},
	}}
	client := setupGenericLogGroup(t, api)

	_, err := ingress.Object[types.ProvisionRequest, LogGroupOutputs](
		client, ServiceName, managedLogGroupKey, "Provision",
	).Request(t.Context(), drivertest.ProvisionRequest(t, LogGroupSpec{
		Account: "test", Region: "us-east-1", LogGroupName: "/praxis/generic", LogGroupClass: "STANDARD",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "immutable")
	assert.Zero(t, api.snapshot().Updates)
}

func TestGenericLogGroupRetriesTransientCreateInsideDurableCallback(t *testing.T) {
	api := &statefulLogGroupAPI{createErrors: []error{&smithy.GenericAPIError{
		Code: "ServiceUnavailableException", Message: "CloudWatch Logs is temporarily unavailable",
	}}}
	client := setupGenericLogGroup(t, api)

	outputs, err := ingress.Object[types.ProvisionRequest, LogGroupOutputs](
		client, ServiceName, managedLogGroupKey, "Provision",
	).Request(t.Context(), drivertest.ProvisionRequest(t, managedLogGroupSpec()))
	require.NoError(t, err)
	assert.Equal(t, "/praxis/generic", outputs.LogGroupName)
	assert.Equal(t, 2, api.snapshot().Creates)
}

func TestGenericLogGroupInvalidCreateIsTerminal(t *testing.T) {
	api := &statefulLogGroupAPI{createErrors: []error{errors.New("InvalidParameterException: bad log group")}}
	client := setupGenericLogGroup(t, api)

	_, err := ingress.Object[types.ProvisionRequest, LogGroupOutputs](
		client, ServiceName, managedLogGroupKey, "Provision",
	).Request(t.Context(), drivertest.ProvisionRequest(t, managedLogGroupSpec()))
	require.Error(t, err)
	assert.Equal(t, 1, api.snapshot().Creates, "terminal validation errors must not be retried")
}

func TestGenericLogGroupDeleteTreatsProviderNotFoundAsSuccess(t *testing.T) {
	api := &statefulLogGroupAPI{}
	client := setupGenericLogGroup(t, api)

	_, err := ingress.Object[types.ProvisionRequest, LogGroupOutputs](
		client, ServiceName, managedLogGroupKey, "Provision",
	).Request(t.Context(), drivertest.ProvisionRequest(t, managedLogGroupSpec()))
	require.NoError(t, err)
	api.mu.Lock()
	api.exists = false
	api.deleteErrors = []error{errors.New("ResourceNotFoundException: already gone")}
	api.mu.Unlock()

	_, err = ingress.Object[restate.Void, restate.Void](
		client, ServiceName, managedLogGroupKey, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, 1, api.snapshot().Deletes)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, ServiceName, managedLogGroupKey, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusDeleted, status.Status)
}
