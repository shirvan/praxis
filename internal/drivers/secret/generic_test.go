package secret

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
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
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	"github.com/shirvan/praxis/internal/eventing"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type statefulSecretAPI struct {
	mu sync.Mutex

	observed               ObservedState
	exists                 bool
	physicalCreates        int
	createAttempts         int
	reads                  int
	updates                int
	deletes                int
	restores               int
	versions               int
	createTokens           map[string]SecretsManagerSecretOutputs
	valueTokens            map[string]struct{}
	failCreateResponseOnce bool
	failValueResponseOnce  bool
	failMetadataOnce       bool
}

func newStatefulSecretAPI() *statefulSecretAPI {
	return &statefulSecretAPI{
		createTokens: map[string]SecretsManagerSecretOutputs{},
		valueTokens:  map[string]struct{}{},
	}
}

func (f *statefulSecretAPI) CreateSecret(_ context.Context, spec SecretsManagerSecretSpec, token string) (SecretsManagerSecretOutputs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createAttempts++
	if outputs, ok := f.createTokens[token]; token != "" && ok {
		return outputs, nil
	}
	if f.exists {
		return SecretsManagerSecretOutputs{}, errors.New("ResourceExistsException: secret already exists")
	}
	f.physicalCreates++
	f.versions++
	kmsKey := spec.KmsKeyID
	if kmsKey == "" {
		kmsKey = "alias/aws/secretsmanager"
	}
	f.observed = ObservedState{
		ARN:  "arn:aws:secretsmanager:us-east-1:123456789012:secret:" + spec.Name + "-ABCDEF",
		Name: spec.Name, Description: spec.Description, KmsKeyID: kmsKey,
		SecretString: spec.SecretString, VersionID: versionID(f.versions),
		Tags: managedTags(spec.Tags, spec.ManagedKey),
	}
	f.exists = true
	outputs := outputsFromObserved(f.observed)
	if token != "" {
		f.createTokens[token] = outputs
	}
	if f.failCreateResponseOnce {
		f.failCreateResponseOnce = false
		return SecretsManagerSecretOutputs{}, errors.New("request timeout after CreateSecret response was lost")
	}
	return outputs, nil
}

func (f *statefulSecretAPI) DescribeSecret(_ context.Context, name string) (ObservedState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if !f.exists || f.observed.Name != name {
		return ObservedState{}, false, nil
	}
	observed := cloneSecretObserved(f.observed)
	if observed.ScheduledForDeletion {
		observed.SecretString = ""
		observed.VersionID = ""
	}
	return observed, true, nil
}

func (f *statefulSecretAPI) UpdateSecret(_ context.Context, name, description, kmsKeyID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.Name != name {
		return errors.New("ResourceNotFoundException: secret does not exist")
	}
	if f.failMetadataOnce {
		f.failMetadataOnce = false
		return errors.New("InvalidParameterException: injected metadata failure")
	}
	f.updates++
	f.observed.Description = description
	if kmsKeyID != "" {
		f.observed.KmsKeyID = kmsKeyID
	}
	return nil
}

func (f *statefulSecretAPI) PutSecretValue(_ context.Context, name, value, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.Name != name {
		return errors.New("ResourceNotFoundException: secret does not exist")
	}
	if f.observed.ScheduledForDeletion {
		return errors.New("InvalidRequestException: secret is scheduled for deletion")
	}
	if _, ok := f.valueTokens[token]; token != "" && ok {
		return nil
	}
	f.updates++
	f.versions++
	f.observed.SecretString = value
	f.observed.VersionID = versionID(f.versions)
	if token != "" {
		f.valueTokens[token] = struct{}{}
	}
	if f.failValueResponseOnce {
		f.failValueResponseOnce = false
		return errors.New("request timeout after PutSecretValue response was lost")
	}
	return nil
}

func (f *statefulSecretAPI) DeleteSecret(_ context.Context, name string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	if !f.exists || f.observed.Name != name {
		return errors.New("ResourceNotFoundException: secret does not exist")
	}
	if f.observed.ScheduledForDeletion {
		return errors.New("InvalidRequestException: secret is already scheduled for deletion")
	}
	if force {
		f.exists = false
		f.observed = ObservedState{}
		return nil
	}
	f.observed.ScheduledForDeletion = true
	return nil
}

func (f *statefulSecretAPI) RestoreSecret(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.Name != name {
		return errors.New("ResourceNotFoundException: secret does not exist")
	}
	if !f.observed.ScheduledForDeletion {
		return errors.New("InvalidRequestException: secret is not scheduled for deletion")
	}
	f.restores++
	f.updates++
	f.observed.ScheduledForDeletion = false
	return nil
}

func (f *statefulSecretAPI) AddTags(_ context.Context, name string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.Name != name {
		return errors.New("ResourceNotFoundException: secret does not exist")
	}
	f.updates++
	if f.observed.Tags == nil {
		f.observed.Tags = map[string]string{}
	}
	maps.Copy(f.observed.Tags, tags)
	return nil
}

func (f *statefulSecretAPI) RemoveTags(_ context.Context, name string, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.Name != name {
		return errors.New("ResourceNotFoundException: secret does not exist")
	}
	f.updates++
	for _, key := range keys {
		delete(f.observed.Tags, key)
	}
	return nil
}

func (f *statefulSecretAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.physicalCreates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulSecretAPI) secret() ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneSecretObserved(f.observed)
}

func (f *statefulSecretAPI) seed(spec SecretsManagerSecretSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exists = true
	f.versions = 1
	f.observed = observedFromSecretSpec(spec, "v1")
}

func (f *statefulSecretAPI) scheduleDeletion() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed.ScheduledForDeletion = true
}

func (f *statefulSecretAPI) forceDeleteExternally() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exists = false
	f.observed = ObservedState{}
}

func (f *statefulSecretAPI) forceCompositeDrift() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed.Description = "stale"
	f.observed.KmsKeyID = "alias/stale"
	f.observed.SecretString = "stale-sensitive-value"
	f.observed.Tags = map[string]string{"env": "stale", "rogue": "remove", "praxis:managed-key": f.observed.Tags["praxis:managed-key"]}
}

type secretDriftSink struct{}

func (secretDriftSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (secretDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

func setupGenericSecret(t *testing.T, api SecretsManagerSecretAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericSecretsManagerSecretDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) SecretsManagerSecretAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(secretDriftSink{})).Ingress()
}

func managedSecretSpec(name string) SecretsManagerSecretSpec {
	return SecretsManagerSecretSpec{
		Account: "test", Region: "us-east-1", Name: name,
		Description: "database credentials", KmsKeyID: "alias/app-key",
		SecretString: "top-secret-value", Tags: map[string]string{"env": "test"},
	}
}

func TestGenericSecretCoreLifecycle(t *testing.T) {
	api := newStatefulSecretAPI()
	client := setupGenericSecret(t, api)
	spec := managedSecretSpec("core-secret")
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[SecretsManagerSecretSpec, SecretsManagerSecretOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~core-secret", Spec: spec, Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, stored SecretsManagerSecretSpec) {
			assert.Equal(t, spec.SecretString, stored.SecretString)
			assert.Equal(t, "us-east-1~core-secret", stored.ManagedKey)
		},
	})
}

func TestGenericSecretObservedImportLifecycle(t *testing.T) {
	api := newStatefulSecretAPI()
	spec := managedSecretSpec("observed-secret")
	api.seed(spec)
	client := setupGenericSecret(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[SecretsManagerSecretOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~observed-secret",
		Ref: types.ImportRef{ResourceID: spec.Name, Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericSecretSensitiveValueBoundary(t *testing.T) {
	api := newStatefulSecretAPI()
	client := setupGenericSecret(t, api)
	spec := managedSecretSpec("sensitive-secret")
	outputs, err := ingress.Object[SecretsManagerSecretSpec, SecretsManagerSecretOutputs](client, ServiceName, "us-east-1~sensitive-secret", "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	status := getSecretStatus(t, client, "us-east-1~sensitive-secret")
	inputs := getSecretInputs(t, client, "us-east-1~sensitive-secret")

	assertJSONDoesNotContain(t, outputs, spec.SecretString)
	assertJSONDoesNotContain(t, status, spec.SecretString)
	assert.Equal(t, spec.SecretString, inputs.SecretString, "GetInputs preserves the existing explicit desired-state contract")
	assert.NotContains(t, outputs.ARN, spec.SecretString)
	assert.NotContains(t, outputs.Name, spec.SecretString)
}

func TestGenericSecretAmbiguousCreateUsesStableRecoveryToken(t *testing.T) {
	api := newStatefulSecretAPI()
	api.failCreateResponseOnce = true
	client := setupGenericSecret(t, api)
	spec := managedSecretSpec("ambiguous-create-secret")

	_, err := ingress.Object[SecretsManagerSecretSpec, SecretsManagerSecretOutputs](client, ServiceName, "us-east-1~ambiguous-create-secret", "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	api.mu.Lock()
	assert.Equal(t, 1, api.physicalCreates)
	assert.Equal(t, 2, api.createAttempts)
	assert.Len(t, api.createTokens, 1)
	for token := range api.createTokens {
		assert.Len(t, token, 64)
	}
	api.mu.Unlock()
}

func TestGenericSecretValueRetryDoesNotCreateDuplicateVersion(t *testing.T) {
	api := newStatefulSecretAPI()
	client := setupGenericSecret(t, api)
	spec := managedSecretSpec("value-retry-secret")
	_, err := ingress.Object[SecretsManagerSecretSpec, SecretsManagerSecretOutputs](client, ServiceName, "us-east-1~value-retry-secret", "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	api.mu.Lock()
	api.failValueResponseOnce = true
	api.mu.Unlock()
	spec.SecretString = "rotated-secret-value"
	_, err = ingress.Object[SecretsManagerSecretSpec, SecretsManagerSecretOutputs](client, ServiceName, "us-east-1~value-retry-secret", "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	api.mu.Lock()
	assert.Equal(t, 2, api.versions, "the retry token must collapse a lost PutSecretValue response into one version")
	api.mu.Unlock()
	assert.Equal(t, "rotated-secret-value", api.secret().SecretString)
}

func TestGenericSecretConvergesAllMutableFields(t *testing.T) {
	api := newStatefulSecretAPI()
	client := setupGenericSecret(t, api)
	spec := managedSecretSpec("mutable-secret")
	_, err := ingress.Object[SecretsManagerSecretSpec, SecretsManagerSecretOutputs](client, ServiceName, "us-east-1~mutable-secret", "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	api.forceCompositeDrift()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, "us-east-1~mutable-secret", "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assertSecretMatchesSpec(t, spec, api.secret())
}

func TestGenericSecretRejectsImmutableNameChange(t *testing.T) {
	api := newStatefulSecretAPI()
	client := setupGenericSecret(t, api)
	spec := managedSecretSpec("immutable-secret")
	_, err := ingress.Object[SecretsManagerSecretSpec, SecretsManagerSecretOutputs](client, ServiceName, "us-east-1~immutable-secret", "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	spec.Name = "different-secret"
	_, err = ingress.Object[SecretsManagerSecretSpec, SecretsManagerSecretOutputs](client, ServiceName, "us-east-1~immutable-secret", "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is immutable")
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericSecretSoftDeleteThenProvisionRestores(t *testing.T) {
	api := newStatefulSecretAPI()
	client := setupGenericSecret(t, api)
	spec := managedSecretSpec("restore-secret")
	key := "us-east-1~restore-secret"
	_, err := ingress.Object[SecretsManagerSecretSpec, SecretsManagerSecretOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, api.secret().ScheduledForDeletion)

	spec.SecretString = "restored-secret-value"
	_, err = ingress.Object[SecretsManagerSecretSpec, SecretsManagerSecretOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.False(t, api.secret().ScheduledForDeletion)
	assert.Equal(t, "restored-secret-value", api.secret().SecretString)
	api.mu.Lock()
	assert.Equal(t, 1, api.physicalCreates)
	assert.Equal(t, 1, api.restores)
	api.mu.Unlock()
}

func TestGenericSecretImportRejectsScheduledDeletion(t *testing.T) {
	api := newStatefulSecretAPI()
	spec := managedSecretSpec("scheduled-import-secret")
	api.seed(spec)
	api.scheduleDeletion()
	client := setupGenericSecret(t, api)
	_, err := ingress.Object[types.ImportRef, SecretsManagerSecretOutputs](client, ServiceName, "us-east-1~scheduled-import-secret", "Import").Request(t.Context(), types.ImportRef{ResourceID: spec.Name, Account: "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restore it first")
	assert.Equal(t, 0, api.snapshot().Updates)
}

func TestGenericSecretRecoversAfterRestoreThenMetadataFault(t *testing.T) {
	api := newStatefulSecretAPI()
	client := setupGenericSecret(t, api)
	spec := managedSecretSpec("partial-restore-secret")
	key := "us-east-1~partial-restore-secret"
	_, err := ingress.Object[SecretsManagerSecretSpec, SecretsManagerSecretOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	api.scheduleDeletion()
	api.mu.Lock()
	api.observed.Description = "stale"
	api.failMetadataOnce = true
	api.mu.Unlock()

	_, err = ingress.Object[SecretsManagerSecretSpec, SecretsManagerSecretOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.False(t, api.secret().ScheduledForDeletion, "the durable restore completed before metadata failed")
	assert.Equal(t, types.StatusError, getSecretStatus(t, client, key).Status)

	_, err = ingress.Object[SecretsManagerSecretSpec, SecretsManagerSecretOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assertSecretMatchesSpec(t, spec, api.secret())
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericSecretExternalDeleteRequiresReplacementWithoutCreation(t *testing.T) {
	api := newStatefulSecretAPI()
	client := setupGenericSecret(t, api)
	spec := managedSecretSpec("external-delete-secret")
	key := "us-east-1~external-delete-secret"
	_, err := ingress.Object[SecretsManagerSecretSpec, SecretsManagerSecretOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	api.forceDeleteExternally()
	before := api.snapshot()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "deleted externally")
	after := api.snapshot()
	assert.Equal(t, before.Creates, after.Creates, "Reconcile reports replacement; it must not recreate the secret")
	assert.Equal(t, types.StatusError, getSecretStatus(t, client, key).Status)
}

func getSecretStatus(t *testing.T, client *ingress.Client, key string) types.StatusResponse {
	t.Helper()
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return status
}

func getSecretInputs(t *testing.T, client *ingress.Client, key string) SecretsManagerSecretSpec {
	t.Helper()
	inputs, err := ingress.Object[restate.Void, SecretsManagerSecretSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return inputs
}

func assertJSONDoesNotContain(t *testing.T, value any, secretValue string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), secretValue)
}

func assertSecretMatchesSpec(t *testing.T, spec SecretsManagerSecretSpec, observed ObservedState) {
	t.Helper()
	assert.Equal(t, spec.Name, observed.Name)
	assert.Equal(t, spec.Description, observed.Description)
	assert.Equal(t, spec.KmsKeyID, observed.KmsKeyID)
	assert.Equal(t, spec.SecretString, observed.SecretString)
	assert.Equal(t, spec.Tags, drivers.FilterPraxisTags(observed.Tags))
}

func observedFromSecretSpec(spec SecretsManagerSecretSpec, version string) ObservedState {
	kmsKey := spec.KmsKeyID
	if kmsKey == "" {
		kmsKey = "alias/aws/secretsmanager"
	}
	return ObservedState{
		ARN:  "arn:aws:secretsmanager:us-east-1:123456789012:secret:" + spec.Name + "-ABCDEF",
		Name: spec.Name, Description: spec.Description, KmsKeyID: kmsKey,
		SecretString: spec.SecretString, VersionID: version,
		Tags: managedTags(spec.Tags, spec.ManagedKey),
	}
}

func cloneSecretObserved(observed ObservedState) ObservedState {
	observed.Tags = maps.Clone(observed.Tags)
	return observed
}

func versionID(version int) string { return "v" + strings.Repeat("0", version-1) + "1" }
