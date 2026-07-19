package ecrrepo

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

type statefulRepositoryAPI struct {
	mu sync.Mutex

	exists   bool
	observed ObservedState
	images   int

	creates      int
	createCalls  int
	reads        int
	updates      int
	deletes      int
	forcedDelete bool

	ambiguousCreate bool
	failPolicyOnce  bool
	readErrors      []error
}

type repositoryTestState struct {
	Exists       bool
	Images       int
	CreateCalls  int
	ForcedDelete bool
}

type ecrRepositoryDriftSink struct{}

func (ecrRepositoryDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }
func (ecrRepositoryDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error {
	return nil
}

func (f *statefulRepositoryAPI) CreateRepository(_ context.Context, spec ECRRepositorySpec) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	if f.exists {
		return ObservedState{}, &mockAPIError{code: "RepositoryAlreadyExistsException", message: "repository already exists"}
	}
	f.creates++
	f.exists = true
	f.observed = observedFromRepositorySpec(spec)
	if f.ambiguousCreate {
		f.ambiguousCreate = false
		return ObservedState{}, errors.New("ServiceUnavailable: response lost after CreateRepository")
	}
	return cloneRepositoryObserved(f.observed), nil
}

func (f *statefulRepositoryAPI) DescribeRepository(_ context.Context, name string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if len(f.readErrors) > 0 {
		err := f.readErrors[0]
		f.readErrors = f.readErrors[1:]
		return ObservedState{}, err
	}
	if !f.exists || f.observed.RepositoryName != name {
		return ObservedState{}, &mockAPIError{code: "RepositoryNotFoundException", message: "repository not found"}
	}
	return cloneRepositoryObserved(f.observed), nil
}

func (f *statefulRepositoryAPI) DeleteRepository(_ context.Context, name string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.RepositoryName != name {
		return &mockAPIError{code: "RepositoryNotFoundException", message: "repository not found"}
	}
	if f.images > 0 && !force {
		return &mockAPIError{code: "RepositoryNotEmptyException", message: "repository contains images"}
	}
	f.deletes++
	f.forcedDelete = force
	if force {
		f.images = 0
	}
	f.exists = false
	f.observed = ObservedState{}
	return nil
}

func (f *statefulRepositoryAPI) UpdateImageTagMutability(_ context.Context, _ string, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.ImageTagMutability = value
	return nil
}

func (f *statefulRepositoryAPI) UpdateScanningConfiguration(_ context.Context, _ string, cfg *ImageScanningConfiguration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.ImageScanningConfiguration = cloneScanning(cfg)
	return nil
}

func (f *statefulRepositoryAPI) PutRepositoryPolicy(_ context.Context, _ string, policy string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPolicyOnce {
		f.failPolicyOnce = false
		return &mockAPIError{code: "InvalidParameterException", message: "injected policy failure"}
	}
	f.updates++
	f.observed.RepositoryPolicy = policy
	return nil
}

func (f *statefulRepositoryAPI) DeleteRepositoryPolicy(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.RepositoryPolicy == "" {
		return &mockAPIError{code: "RepositoryPolicyNotFoundException", message: "policy not found"}
	}
	f.updates++
	f.observed.RepositoryPolicy = ""
	return nil
}

func (f *statefulRepositoryAPI) UpdateTags(_ context.Context, _ string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.Tags = maps.Clone(tags)
	return nil
}

func (f *statefulRepositoryAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulRepositoryAPI) repository() ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneRepositoryObserved(f.observed)
}

func (f *statefulRepositoryAPI) testState() repositoryTestState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return repositoryTestState{
		Exists: f.exists, Images: f.images, CreateCalls: f.createCalls, ForcedDelete: f.forcedDelete,
	}
}

func (f *statefulRepositoryAPI) removeExternally() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exists = false
	f.observed = ObservedState{}
}

func setupGenericECRRepository(t *testing.T, api RepositoryAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericECRRepositoryDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) RepositoryAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(ecrRepositoryDriftSink{})).Ingress()
}

func managedRepositorySpec(name string) ECRRepositorySpec {
	return ECRRepositorySpec{
		Account: "test", Region: "us-east-1", RepositoryName: name,
		ImageTagMutability:         "IMMUTABLE",
		ImageScanningConfiguration: &ImageScanningConfiguration{ScanOnPush: true},
		EncryptionConfiguration:    &EncryptionConfiguration{EncryptionType: "AES256"},
		RepositoryPolicy:           `{"Version":"2012-10-17","Statement":[]}`,
		Tags:                       map[string]string{"env": "prod", "team": "platform"},
	}
}

func TestGenericECRRepositoryCoreLifecycle(t *testing.T) {
	api := &statefulRepositoryAPI{}
	client := setupGenericECRRepository(t, api)
	spec := managedRepositorySpec("generic-repository")
	key := "us-east-1~generic-repository"
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[ECRRepositorySpec, ECRRepositoryOutputs]{
		Client: client, ServiceName: ServiceName, Key: key, Spec: spec, Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs ECRRepositorySpec) {
			assert.Equal(t, key, inputs.ManagedKey)
			assert.Equal(t, spec.Tags, inputs.Tags)
		},
	})
}

func TestGenericECRRepositoryObservedImportLifecycle(t *testing.T) {
	spec := managedRepositorySpec("existing-repository")
	api := &statefulRepositoryAPI{exists: true, observed: observedFromRepositorySpec(spec)}
	client := setupGenericECRRepository(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[ECRRepositoryOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~existing-repository",
		Ref: types.ImportRef{ResourceID: spec.RepositoryName, Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericECRRepositoryConvergesMutableConfigurationPolicyAndTags(t *testing.T) {
	api := &statefulRepositoryAPI{}
	client := setupGenericECRRepository(t, api)
	spec := managedRepositorySpec("drift-repository")
	key := "us-east-1~drift-repository"
	_, err := ingress.Object[types.ProvisionRequest, ECRRepositoryOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)

	api.mu.Lock()
	api.observed.ImageTagMutability = "MUTABLE"
	api.observed.ImageScanningConfiguration = &ImageScanningConfiguration{ScanOnPush: false}
	api.observed.RepositoryPolicy = `{"Version":"2008-10-17"}`
	api.observed.Tags = map[string]string{"env": "dev", "stale": "remove", "praxis:managed-key": key}
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	observed := api.repository()
	assert.Equal(t, spec.ImageTagMutability, observed.ImageTagMutability)
	assert.True(t, scanningEqual(spec.ImageScanningConfiguration, observed.ImageScanningConfiguration))
	assert.Equal(t, normalizeJSON(spec.RepositoryPolicy), normalizeJSON(observed.RepositoryPolicy))
	assert.Equal(t, tagsForApply(spec.Tags, key), observed.Tags)
}

func TestGenericECRRepositoryRemovesOwnedPolicyWhenOmitted(t *testing.T) {
	api := &statefulRepositoryAPI{}
	client := setupGenericECRRepository(t, api)
	spec := managedRepositorySpec("policy-removal")
	key := "us-east-1~policy-removal"
	_, err := ingress.Object[types.ProvisionRequest, ECRRepositoryOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	spec.RepositoryPolicy = ""
	_, err = ingress.Object[types.ProvisionRequest, ECRRepositoryOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Empty(t, api.repository().RepositoryPolicy)
}

func TestGenericECRRepositoryRejectsImmutableIdentityAndEncryption(t *testing.T) {
	t.Run("repository name", func(t *testing.T) {
		api := &statefulRepositoryAPI{}
		client := setupGenericECRRepository(t, api)
		spec := managedRepositorySpec("original-repository")
		key := "us-east-1~original-repository"
		_, err := ingress.Object[types.ProvisionRequest, ECRRepositoryOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
		require.NoError(t, err)
		spec.RepositoryName = "different-repository"
		_, err = ingress.Object[types.ProvisionRequest, ECRRepositoryOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "repositoryName is immutable")
		assert.Equal(t, 1, api.snapshot().Creates)
	})

	t.Run("encryption", func(t *testing.T) {
		api := &statefulRepositoryAPI{}
		client := setupGenericECRRepository(t, api)
		spec := managedRepositorySpec("encrypted-repository")
		key := "us-east-1~encrypted-repository"
		_, err := ingress.Object[types.ProvisionRequest, ECRRepositoryOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
		require.NoError(t, err)
		spec.EncryptionConfiguration = &EncryptionConfiguration{EncryptionType: "KMS", KmsKey: "arn:aws:kms:us-east-1:123456789012:key/example"}
		_, err = ingress.Object[types.ProvisionRequest, ECRRepositoryOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "encryptionConfiguration is immutable")
		assert.Equal(t, 1, api.snapshot().Creates)
	})
}

func TestGenericECRRepositoryRecoversPartialCreateWithoutSecondRepository(t *testing.T) {
	api := &statefulRepositoryAPI{failPolicyOnce: true}
	client := setupGenericECRRepository(t, api)
	spec := managedRepositorySpec("partial-repository")
	key := "us-east-1~partial-repository"
	_, err := ingress.Object[types.ProvisionRequest, ECRRepositoryOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.True(t, api.testState().Exists)

	_, err = ingress.Object[types.ProvisionRequest, ECRRepositoryOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.Equal(t, normalizeJSON(spec.RepositoryPolicy), normalizeJSON(api.repository().RepositoryPolicy))
}

func TestGenericECRRepositoryRecoversAmbiguousCreateResponse(t *testing.T) {
	api := &statefulRepositoryAPI{ambiguousCreate: true}
	client := setupGenericECRRepository(t, api)
	spec := managedRepositorySpec("ambiguous-repository")
	key := "us-east-1~ambiguous-repository"
	outputs, err := ingress.Object[types.ProvisionRequest, ECRRepositoryOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, spec.RepositoryName, outputs.RepositoryName)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.GreaterOrEqual(t, api.testState().CreateCalls, 2)
}

func TestGenericECRRepositoryRetriesTransientObservation(t *testing.T) {
	spec := managedRepositorySpec("read-retry")
	api := &statefulRepositoryAPI{
		exists: true, observed: observedFromRepositorySpec(spec),
		readErrors: []error{errors.New("ServiceUnavailable: transient describe failure")},
	}
	client := setupGenericECRRepository(t, api)
	_, err := ingress.Object[types.ProvisionRequest, ECRRepositoryOutputs](client, ServiceName, "us-east-1~read-retry", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Zero(t, api.snapshot().Creates)
	assert.GreaterOrEqual(t, api.snapshot().Reads, 2)
}

func TestGenericECRRepositoryExternalDeleteRequiresReplacementWithoutCreate(t *testing.T) {
	api := &statefulRepositoryAPI{}
	client := setupGenericECRRepository(t, api)
	spec := managedRepositorySpec("deleted-repository")
	key := "us-east-1~deleted-repository"
	_, err := ingress.Object[types.ProvisionRequest, ECRRepositoryOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	creates := api.snapshot().Creates
	api.removeExternally()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "ECRRepository resource was deleted externally")
	assert.Equal(t, creates, api.snapshot().Creates)
}

func TestGenericECRRepositoryDeleteDoesNotRemoveImagesImplicitly(t *testing.T) {
	api := &statefulRepositoryAPI{images: 2}
	client := setupGenericECRRepository(t, api)
	spec := managedRepositorySpec("non-empty-repository")
	key := "us-east-1~non-empty-repository"
	_, err := ingress.Object[types.ProvisionRequest, ECRRepositoryOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repository contains images")
	state := api.testState()
	assert.True(t, state.Exists)
	assert.Equal(t, 2, state.Images)
	assert.Zero(t, api.snapshot().Deletes)
}

func TestGenericECRRepositoryForceDeleteIsExplicit(t *testing.T) {
	api := &statefulRepositoryAPI{images: 2}
	client := setupGenericECRRepository(t, api)
	spec := managedRepositorySpec("forced-repository")
	spec.ForceDelete = true
	key := "us-east-1~forced-repository"
	_, err := ingress.Object[types.ProvisionRequest, ECRRepositoryOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	state := api.testState()
	assert.True(t, state.ForcedDelete)
	assert.Zero(t, state.Images)
}

func observedFromRepositorySpec(spec ECRRepositorySpec) ObservedState {
	return ObservedState{
		RepositoryArn:              "arn:aws:ecr:us-east-1:123456789012:repository/" + spec.RepositoryName,
		RepositoryName:             spec.RepositoryName,
		RepositoryUri:              "123456789012.dkr.ecr.us-east-1.amazonaws.com/" + spec.RepositoryName,
		RegistryId:                 "123456789012",
		ImageTagMutability:         spec.ImageTagMutability,
		ImageScanningConfiguration: cloneScanning(spec.ImageScanningConfiguration),
		EncryptionConfiguration:    cloneEncryption(spec.EncryptionConfiguration),
		Tags:                       tagsForApply(spec.Tags, spec.ManagedKey),
	}
}

func cloneRepositoryObserved(input ObservedState) ObservedState {
	input.ImageScanningConfiguration = cloneScanning(input.ImageScanningConfiguration)
	input.EncryptionConfiguration = cloneEncryption(input.EncryptionConfiguration)
	input.Tags = maps.Clone(input.Tags)
	return input
}

func cloneScanning(input *ImageScanningConfiguration) *ImageScanningConfiguration {
	if input == nil {
		return nil
	}
	copy := *input
	return &copy
}

func cloneEncryption(input *EncryptionConfiguration) *EncryptionConfiguration {
	if input == nil {
		return nil
	}
	copy := *input
	return &copy
}
