package ecrpolicy

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

type statefulLifecyclePolicyAPI struct {
	mu sync.Mutex

	repositoryExists bool
	repositoryName   string
	repositoryARN    string
	registryID       string
	policy           string
	repositoryData   map[string]string

	reads       int
	putCalls    int
	deleteCalls int
	creates     int
	updates     int
	deletes     int

	getErrors              []error
	putErrors              []error
	putAfterApplyErrors    []error
	deleteErrors           []error
	deleteAfterApplyErrors []error
}

type lifecyclePolicyDriftSink struct{}

func (lifecyclePolicyDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }

func (lifecyclePolicyDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error {
	return nil
}

func newStatefulLifecyclePolicyAPI(name, policy string) *statefulLifecyclePolicyAPI {
	return &statefulLifecyclePolicyAPI{
		repositoryExists: true, repositoryName: name,
		repositoryARN: "arn:aws:ecr:us-east-1:123456789012:repository/" + name,
		registryID:    "123456789012", policy: policy,
		repositoryData: map[string]string{"imageTagMutability": "IMMUTABLE", "team": "platform"},
	}
}

func (f *statefulLifecyclePolicyAPI) PutLifecyclePolicy(_ context.Context, spec ECRLifecyclePolicySpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putCalls++
	if len(f.putErrors) > 0 {
		err := f.putErrors[0]
		f.putErrors = f.putErrors[1:]
		return err
	}
	if !f.repositoryExists || f.repositoryName != spec.RepositoryName {
		return errors.New("RepositoryNotFoundException: repository missing")
	}
	if f.policy == "" {
		f.creates++
	} else if normalizePolicy(f.policy) != normalizePolicy(spec.LifecyclePolicyText) {
		f.updates++
	}
	f.policy = spec.LifecyclePolicyText
	if len(f.putAfterApplyErrors) > 0 {
		err := f.putAfterApplyErrors[0]
		f.putAfterApplyErrors = f.putAfterApplyErrors[1:]
		return err
	}
	return nil
}

func (f *statefulLifecyclePolicyAPI) GetLifecyclePolicy(_ context.Context, repositoryName string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if len(f.getErrors) > 0 {
		err := f.getErrors[0]
		f.getErrors = f.getErrors[1:]
		return ObservedState{}, err
	}
	if !f.repositoryExists || f.repositoryName != repositoryName {
		return ObservedState{}, errors.New("RepositoryNotFoundException: repository missing")
	}
	if f.policy == "" {
		return ObservedState{}, errors.New("LifecyclePolicyNotFoundException: policy missing")
	}
	return ObservedState{
		RepositoryName: f.repositoryName, RepositoryArn: f.repositoryARN,
		RegistryId: f.registryID, LifecyclePolicyText: f.policy,
	}, nil
}

func (f *statefulLifecyclePolicyAPI) DeleteLifecyclePolicy(_ context.Context, repositoryName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	if len(f.deleteErrors) > 0 {
		err := f.deleteErrors[0]
		f.deleteErrors = f.deleteErrors[1:]
		return err
	}
	if !f.repositoryExists || f.repositoryName != repositoryName {
		return errors.New("RepositoryNotFoundException: repository missing")
	}
	if f.policy == "" {
		return errors.New("LifecyclePolicyNotFoundException: policy missing")
	}
	f.policy = ""
	f.deletes++
	if len(f.deleteAfterApplyErrors) > 0 {
		err := f.deleteAfterApplyErrors[0]
		f.deleteAfterApplyErrors = f.deleteAfterApplyErrors[1:]
		return err
	}
	return nil
}

func (f *statefulLifecyclePolicyAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

type lifecyclePolicySnapshot struct {
	repositoryExists bool
	policy           string
	repositoryData   map[string]string
	putCalls         int
	deleteCalls      int
}

func (f *statefulLifecyclePolicyAPI) current() lifecyclePolicySnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return lifecyclePolicySnapshot{
		repositoryExists: f.repositoryExists, policy: f.policy,
		repositoryData: maps.Clone(f.repositoryData), putCalls: f.putCalls, deleteCalls: f.deleteCalls,
	}
}

func setupGenericLifecyclePolicy(t *testing.T, api LifecyclePolicyAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericECRLifecyclePolicyDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) LifecyclePolicyAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(lifecyclePolicyDriftSink{})).Ingress()
}

const (
	managedLifecyclePolicyKey = "us-east-1~generic-repo"
	managedLifecyclePolicy    = `{"rules":[{"rulePriority":1,"selection":{"tagStatus":"untagged","countType":"imageCountMoreThan","countNumber":5},"action":{"type":"expire"}}]}`
)

func managedLifecyclePolicySpec() ECRLifecyclePolicySpec {
	return ECRLifecyclePolicySpec{
		Account: "test", Region: "us-east-1", RepositoryName: "generic-repo",
		LifecyclePolicyText: managedLifecyclePolicy,
	}
}

func TestGenericECRLifecyclePolicyCoreLifecycle(t *testing.T) {
	api := newStatefulLifecyclePolicyAPI("generic-repo", "")
	client := setupGenericLifecyclePolicy(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[ECRLifecyclePolicySpec, ECRLifecyclePolicyOutputs]{
		Client: client, ServiceName: ServiceName, Key: managedLifecyclePolicyKey,
		Spec: managedLifecyclePolicySpec(), Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs ECRLifecyclePolicySpec) {
			assert.Equal(t, managedLifecyclePolicyKey, inputs.ManagedKey)
		},
	})
	current := api.current()
	assert.True(t, current.repositoryExists, "policy deletion must preserve the repository")
	assert.Equal(t, map[string]string{"imageTagMutability": "IMMUTABLE", "team": "platform"}, current.repositoryData)
}

func TestGenericECRLifecyclePolicyObservedImportLifecycle(t *testing.T) {
	api := newStatefulLifecyclePolicyAPI("existing-repo", `{"rules":[]}`)
	client := setupGenericLifecyclePolicy(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[ECRLifecyclePolicyOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~existing-repo",
		Ref: types.ImportRef{ResourceID: api.repositoryARN, Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericECRLifecyclePolicyFormattingOnlyDifferenceDoesNotWrite(t *testing.T) {
	api := newStatefulLifecyclePolicyAPI("generic-repo", `{ "rules" : [ { "action" : { "type" : "expire" }, "selection" : { "countNumber" : 5, "countType" : "imageCountMoreThan", "tagStatus" : "untagged" }, "rulePriority" : 1 } ] }`)
	client := setupGenericLifecyclePolicy(t, api)
	before := api.current()

	_, err := ingress.Object[types.ProvisionRequest, ECRLifecyclePolicyOutputs](client, ServiceName, managedLifecyclePolicyKey, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedLifecyclePolicySpec()))
	require.NoError(t, err)
	assert.Equal(t, before.putCalls, api.current().putCalls, "canonical JSON equality must avoid PutLifecyclePolicy")
}

func TestGenericECRLifecyclePolicyRecoversAmbiguousPutAcrossProvision(t *testing.T) {
	api := newStatefulLifecyclePolicyAPI("generic-repo", "")
	api.putAfterApplyErrors = []error{errors.New("AccessDenied: response lost after policy write")}
	client := setupGenericLifecyclePolicy(t, api)

	_, err := ingress.Object[types.ProvisionRequest, ECRLifecyclePolicyOutputs](client, ServiceName, managedLifecyclePolicyKey, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedLifecyclePolicySpec()))
	require.Error(t, err)
	assert.Equal(t, 1, api.current().putCalls)
	assert.Equal(t, normalizePolicy(managedLifecyclePolicy), normalizePolicy(api.current().policy))

	_, err = ingress.Object[types.ProvisionRequest, ECRLifecyclePolicyOutputs](client, ServiceName, managedLifecyclePolicyKey, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedLifecyclePolicySpec()))
	require.NoError(t, err)
	assert.Equal(t, 1, api.current().putCalls, "observe-before-create must recover the completed policy")
}

func TestGenericECRLifecyclePolicyRetriesAmbiguousPutIdempotently(t *testing.T) {
	api := newStatefulLifecyclePolicyAPI("generic-repo", "")
	api.putAfterApplyErrors = []error{&smithy.GenericAPIError{Code: "ServerException", Message: "response lost"}}
	client := setupGenericLifecyclePolicy(t, api)

	_, err := ingress.Object[types.ProvisionRequest, ECRLifecyclePolicyOutputs](client, ServiceName, managedLifecyclePolicyKey, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedLifecyclePolicySpec()))
	require.NoError(t, err)
	assert.Equal(t, 2, api.current().putCalls)
	assert.Equal(t, 1, api.snapshot().Creates, "retry must replace the same policy subresource")
}

func TestGenericECRLifecyclePolicyRecoversAmbiguousDelete(t *testing.T) {
	api := newStatefulLifecyclePolicyAPI("generic-repo", "")
	client := setupGenericLifecyclePolicy(t, api)
	_, err := ingress.Object[types.ProvisionRequest, ECRLifecyclePolicyOutputs](client, ServiceName, managedLifecyclePolicyKey, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedLifecyclePolicySpec()))
	require.NoError(t, err)
	api.deleteAfterApplyErrors = []error{&smithy.GenericAPIError{Code: "ServerException", Message: "delete response lost"}}

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, managedLifecyclePolicyKey, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, 2, api.current().deleteCalls, "retry must treat the already-removed policy as success")
	assert.True(t, api.current().repositoryExists)
}

func TestGenericECRLifecyclePolicyManagedReconcileCorrectsOnlyPolicy(t *testing.T) {
	api := newStatefulLifecyclePolicyAPI("generic-repo", "")
	client := setupGenericLifecyclePolicy(t, api)
	_, err := ingress.Object[types.ProvisionRequest, ECRLifecyclePolicyOutputs](client, ServiceName, managedLifecyclePolicyKey, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedLifecyclePolicySpec()))
	require.NoError(t, err)
	api.mu.Lock()
	api.policy = `{"rules":[]}`
	api.repositoryData["imageTagMutability"] = "MUTABLE"
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, managedLifecyclePolicyKey, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	current := api.current()
	assert.Equal(t, normalizePolicy(managedLifecyclePolicy), normalizePolicy(current.policy))
	assert.Equal(t, "MUTABLE", current.repositoryData["imageTagMutability"], "policy convergence must not mutate repository settings")
}

func TestGenericECRLifecyclePolicyExternalRemovalRequiresReplacementWithoutPut(t *testing.T) {
	api := newStatefulLifecyclePolicyAPI("generic-repo", "")
	client := setupGenericLifecyclePolicy(t, api)
	_, err := ingress.Object[types.ProvisionRequest, ECRLifecyclePolicyOutputs](client, ServiceName, managedLifecyclePolicyKey, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedLifecyclePolicySpec()))
	require.NoError(t, err)
	api.mu.Lock()
	api.policy = ""
	api.mu.Unlock()
	before := api.current()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, managedLifecyclePolicyKey, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "deleted externally")
	assert.Equal(t, before.putCalls, api.current().putCalls, "Reconcile must not recreate a removed policy")
}

func TestGenericECRLifecyclePolicyMissingRepositoryIsNeverCreated(t *testing.T) {
	api := newStatefulLifecyclePolicyAPI("generic-repo", "")
	api.repositoryExists = false
	client := setupGenericLifecyclePolicy(t, api)

	_, err := ingress.Object[types.ProvisionRequest, ECRLifecyclePolicyOutputs](client, ServiceName, managedLifecyclePolicyKey, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedLifecyclePolicySpec()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RepositoryNotFoundException")
	assert.Equal(t, 1, api.current().putCalls, "the driver may attempt policy creation but has no repository-create API")
	assert.False(t, api.current().repositoryExists)
}

func TestGenericECRLifecyclePolicyImportRejectsMissingPolicy(t *testing.T) {
	api := newStatefulLifecyclePolicyAPI("generic-repo", "")
	client := setupGenericLifecyclePolicy(t, api)

	_, err := ingress.Object[types.ImportRef, ECRLifecyclePolicyOutputs](client, ServiceName, managedLifecyclePolicyKey, "Import").Request(
		t.Context(), types.ImportRef{ResourceID: "generic-repo", Account: "test"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
	assert.Zero(t, api.current().putCalls, "Import must remain read-only")
}

func TestGenericECRLifecyclePolicyCannotRetargetRepository(t *testing.T) {
	api := newStatefulLifecyclePolicyAPI("generic-repo", "")
	client := setupGenericLifecyclePolicy(t, api)
	_, err := ingress.Object[types.ProvisionRequest, ECRLifecyclePolicyOutputs](client, ServiceName, managedLifecyclePolicyKey, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedLifecyclePolicySpec()))
	require.NoError(t, err)
	before := api.current()

	spec := managedLifecyclePolicySpec()
	spec.RepositoryName = "different-repo"
	_, err = ingress.Object[types.ProvisionRequest, ECRLifecyclePolicyOutputs](client, ServiceName, managedLifecyclePolicyKey, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repositoryName is immutable")
	assert.Equal(t, before.putCalls, api.current().putCalls)
}

func TestGenericECRLifecyclePolicyAcceptsValidNonObjectJSON(t *testing.T) {
	api := newStatefulLifecyclePolicyAPI("generic-repo", "")
	client := setupGenericLifecyclePolicy(t, api)
	spec := managedLifecyclePolicySpec()
	spec.LifecyclePolicyText = `[]`

	_, err := ingress.Object[types.ProvisionRequest, ECRLifecyclePolicyOutputs](client, ServiceName, managedLifecyclePolicyKey, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, 1, api.current().putCalls)
	assert.Equal(t, `[]`, api.current().policy)
}
