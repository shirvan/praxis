package keypair

import (
	"context"
	"encoding/json"
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
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	"github.com/shirvan/praxis/internal/drivers/kernel"
	"github.com/shirvan/praxis/internal/eventing"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

const generatedPrivateKey = "-----BEGIN PRIVATE KEY-----generated-once-----END PRIVATE KEY-----"

type keyPairDriftSink struct{}

func (keyPairDriftSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (keyPairDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

// keyPairTestDriver exposes the exact StateKey JSON only inside this package's
// test service so the secret non-persistence boundary is asserted directly.
type keyPairTestDriver struct {
	*kernel.Driver[KeyPairSpec, KeyPairOutputs, ObservedState]
}

func (d keyPairTestDriver) RawState(ctx restate.ObjectSharedContext) (string, error) {
	raw, err := restate.Get[json.RawMessage](ctx, drivers.StateKey)
	return string(raw), err
}

type statefulKeyPairAPI struct {
	mu sync.Mutex

	items   map[string]ObservedState
	creates int
	reads   int
	updates int
	deletes int
	nextID  int

	createErrors []error
}

func newStatefulKeyPairAPI() *statefulKeyPairAPI {
	return &statefulKeyPairAPI{items: map[string]ObservedState{}}
}

func (f *statefulKeyPairAPI) CreateKeyPair(_ context.Context, name, keyType string, tags map[string]string) (string, string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.items[name]; exists {
		return "", "", "", &mockAPIError{code: "InvalidKeyPair.Duplicate", message: "duplicate"}
	}
	f.creates++
	f.nextID++
	id := "key-generated"
	if f.nextID > 1 {
		id = "key-duplicate"
	}
	f.items[name] = ObservedState{
		KeyName: name, KeyPairId: id, KeyFingerprint: "fingerprint-generated",
		KeyType: keyType, Tags: maps.Clone(tags),
	}
	if len(f.createErrors) > 0 {
		err := f.createErrors[0]
		f.createErrors = f.createErrors[1:]
		return "", "", "", err
	}
	return id, "fingerprint-generated", generatedPrivateKey, nil
}

func (f *statefulKeyPairAPI) ImportKeyPair(_ context.Context, name, _ string, tags map[string]string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.items[name]; exists {
		return "", "", &mockAPIError{code: "InvalidKeyPair.Duplicate", message: "duplicate"}
	}
	f.creates++
	f.nextID++
	id := "key-imported"
	f.items[name] = ObservedState{
		KeyName: name, KeyPairId: id, KeyFingerprint: "fingerprint-imported",
		KeyType: "rsa", Tags: maps.Clone(tags),
	}
	return id, "fingerprint-imported", nil
}

func (f *statefulKeyPairAPI) DescribeKeyPair(_ context.Context, name string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	observed, exists := f.items[name]
	if !exists {
		return ObservedState{}, awserr.NotFound("key pair not found")
	}
	observed.Tags = maps.Clone(observed.Tags)
	return observed, nil
}

func (f *statefulKeyPairAPI) DeleteKeyPair(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.items[name]; !exists {
		return awserr.NotFound("key pair not found")
	}
	delete(f.items, name)
	f.deletes++
	return nil
}

func (f *statefulKeyPairAPI) UpdateTags(_ context.Context, id string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, observed := range f.items {
		if observed.KeyPairId == id {
			observed.Tags = maps.Clone(tags)
			f.items[name] = observed
			f.updates++
			return nil
		}
	}
	return awserr.NotFound("key pair not found")
}

func (f *statefulKeyPairAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulKeyPairAPI) seed(observed ObservedState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed.Tags = maps.Clone(observed.Tags)
	f.items[observed.KeyName] = observed
}

func (f *statefulKeyPairAPI) remove(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, name)
}

func setupGenericKeyPair(t *testing.T, api KeyPairAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericKeyPairDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) KeyPairAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(keyPairTestDriver{Driver: driver}), restate.Reflect(keyPairDriftSink{})).Ingress()
}

func managedKeyPairSpec(name string) KeyPairSpec {
	return KeyPairSpec{
		Account: "test", Region: "us-east-1", KeyName: name, KeyType: "rsa",
		PublicKeyMaterial: "ssh-rsa test-public-key", Tags: map[string]string{"env": "test"},
	}
}

func TestGenericKeyPairCoreLifecycle(t *testing.T) {
	api := newStatefulKeyPairAPI()
	client := setupGenericKeyPair(t, api)
	key := "us-east-1~generic-key"
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[KeyPairSpec, KeyPairOutputs]{
		Client: client, ServiceName: ServiceName, Key: key, Spec: managedKeyPairSpec("generic-key"), Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs KeyPairSpec) {
			assert.Equal(t, key, inputs.ManagedKey)
			assert.Equal(t, managedKeyPairSpec("generic-key").PublicKeyMaterial, inputs.PublicKeyMaterial)
		},
	})
}

func TestGenericKeyPairObservedImportLifecycle(t *testing.T) {
	api := newStatefulKeyPairAPI()
	api.seed(ObservedState{
		KeyName: "existing-key", KeyPairId: "key-existing", KeyFingerprint: "fingerprint",
		KeyType: "rsa", Tags: map[string]string{"env": "import"},
	})
	client := setupGenericKeyPair(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[KeyPairOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~existing-key",
		Ref: types.ImportRef{Account: "test", ResourceID: "existing-key"}, Snapshot: api.snapshot,
	})
}

func TestGenericKeyPairGeneratedPrivateKeyIsCreateOnly(t *testing.T) {
	api := newStatefulKeyPairAPI()
	client := setupGenericKeyPair(t, api)
	key := "us-east-1~generated-key"
	spec := managedKeyPairSpec("generated-key")
	spec.PublicKeyMaterial = ""

	first, err := ingress.Object[types.ProvisionRequest, KeyPairOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, generatedPrivateKey, first.PrivateKeyMaterial)

	// Restate journals the initial handler response for durable replay, so the
	// private key is not absent from the system entirely. The security boundary
	// is that it never enters driver K/V and is never returned by later calls.
	stored, err := ingress.Object[restate.Void, KeyPairOutputs](client, ServiceName, key, "GetOutputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Empty(t, stored.PrivateKeyMaterial)
	assert.Equal(t, first.KeyPairId, stored.KeyPairId)
	rawState, err := ingress.Object[restate.Void, string](client, ServiceName, key, "RawState").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.NotContains(t, rawState, generatedPrivateKey, "StateKey envelope must never contain generated private material")

	second, err := ingress.Object[types.ProvisionRequest, KeyPairOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Empty(t, second.PrivateKeyMaterial)
	assert.Equal(t, first.KeyPairId, second.KeyPairId)
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericKeyPairRecoversAmbiguousCreateWithoutDuplicate(t *testing.T) {
	api := newStatefulKeyPairAPI()
	api.createErrors = []error{errors.New("create response lost")}
	client := setupGenericKeyPair(t, api)
	spec := managedKeyPairSpec("ambiguous-key")
	spec.PublicKeyMaterial = ""

	outputs, err := ingress.Object[types.ProvisionRequest, KeyPairOutputs](client, ServiceName, "us-east-1~ambiguous-key", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, "key-generated", outputs.KeyPairId)
	assert.Empty(t, outputs.PrivateKeyMaterial, "a provider response lost before journaling cannot safely recover private material")
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericKeyPairRejectsUnownedNameCollision(t *testing.T) {
	api := newStatefulKeyPairAPI()
	api.seed(ObservedState{KeyName: "collision", KeyPairId: "key-other", KeyType: "rsa", Tags: map[string]string{}})
	client := setupGenericKeyPair(t, api)
	_, err := ingress.Object[types.ProvisionRequest, KeyPairOutputs](client, ServiceName, "us-east-1~collision", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedKeyPairSpec("collision")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exact Praxis ownership")
	assert.Equal(t, 0, api.snapshot().Creates)
}

func TestGenericKeyPairRejectsImmutableTypeAndPublicMaterial(t *testing.T) {
	api := newStatefulKeyPairAPI()
	client := setupGenericKeyPair(t, api)
	key := "us-east-1~immutable-key"
	spec := managedKeyPairSpec("immutable-key")
	_, err := ingress.Object[types.ProvisionRequest, KeyPairOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	original := spec

	changedName := spec
	changedName.KeyName = "renamed-key"
	_, err = ingress.Object[types.ProvisionRequest, KeyPairOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, changedName))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keyName is immutable")
	storedInputs, inputErr := ingress.Object[restate.Void, KeyPairSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, inputErr)
	assert.Equal(t, original.KeyName, storedInputs.KeyName)

	changedType := spec
	changedType.KeyType = "ed25519"
	_, err = ingress.Object[types.ProvisionRequest, KeyPairOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, changedType))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keyType is immutable")
	storedInputs, inputErr = ingress.Object[restate.Void, KeyPairSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, inputErr)
	assert.Equal(t, original.KeyType, storedInputs.KeyType)

	changedMaterial := spec
	changedMaterial.PublicKeyMaterial = "ssh-rsa different-public-key"
	_, err = ingress.Object[types.ProvisionRequest, KeyPairOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, changedMaterial))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publicKeyMaterial is create-only")
	storedInputs, inputErr = ingress.Object[restate.Void, KeyPairSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, inputErr)
	assert.Equal(t, original.PublicKeyMaterial, storedInputs.PublicKeyMaterial)
}

func TestGenericKeyPairProvisionChangeRejectsEveryCreateOnlyField(t *testing.T) {
	previous := managedKeyPairSpec("identity-key")
	cases := map[string]func(*KeyPairSpec){
		"account":  func(spec *KeyPairSpec) { spec.Account = "other" },
		"region":   func(spec *KeyPairSpec) { spec.Region = "us-west-2" },
		"name":     func(spec *KeyPairSpec) { spec.KeyName = "other-key" },
		"type":     func(spec *KeyPairSpec) { spec.KeyType = "ed25519" },
		"material": func(spec *KeyPairSpec) { spec.PublicKeyMaterial = "ssh-rsa other" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			next := previous
			mutate(&next)
			err := (&genericOperations{}).ConvergeProvisionChange(nil, previous, next, ObservedState{})
			require.Error(t, err)
			assert.EqualValues(t, 409, restate.ErrorCode(err))
		})
	}
}

func TestGenericKeyPairExternalDeleteRequiresExplicitProvision(t *testing.T) {
	api := newStatefulKeyPairAPI()
	client := setupGenericKeyPair(t, api)
	key := "us-east-1~external-delete"
	_, err := ingress.Object[types.ProvisionRequest, KeyPairOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedKeyPairSpec("external-delete")))
	require.NoError(t, err)
	before := api.snapshot()
	api.remove("external-delete")
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "deleted externally")
	assert.Equal(t, before.Creates, api.snapshot().Creates)
}
