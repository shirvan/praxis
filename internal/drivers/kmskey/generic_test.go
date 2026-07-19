package kmskey

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
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type statefulKMSAPI struct {
	mu       sync.Mutex
	observed ObservedState
	creates  int
	reads    int
	updates  int
	deletes  int
}

func (f *statefulKMSAPI) CreateKey(_ context.Context, spec KMSKeySpec) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	tags := maps.Clone(spec.Tags)
	if tags == nil {
		tags = map[string]string{}
	}
	tags["praxis:managed-key"] = spec.ManagedKey
	f.observed = ObservedState{
		ARN: "arn:aws:kms:us-east-1:123456789012:key/key-1", KeyID: "key-1",
		Description: spec.Description, KeyUsage: spec.KeyUsage, KeySpec: spec.KeySpec,
		KeyState: "Enabled", Enabled: true, Tags: tags,
	}
	return f.observed.KeyID, f.observed.ARN, nil
}

func (f *statefulKMSAPI) CreateAlias(_ context.Context, alias, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed.AliasName = alias
	return nil
}

func (f *statefulKMSAPI) DescribeKey(_ context.Context, alias string) (ObservedState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.observed, f.observed.AliasName == alias, nil
}

func (f *statefulKMSAPI) UpdateDescription(_ context.Context, _ string, description string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.Description = description
	return nil
}

func (f *statefulKMSAPI) EnableKeyRotation(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.EnableKeyRotation = true
	return nil
}

func (f *statefulKMSAPI) DisableKeyRotation(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.EnableKeyRotation = false
	return nil
}

func (f *statefulKMSAPI) TagResource(_ context.Context, _ string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	if f.observed.Tags == nil {
		f.observed.Tags = map[string]string{}
	}
	maps.Copy(f.observed.Tags, tags)
	return nil
}

func (f *statefulKMSAPI) UntagResource(_ context.Context, _ string, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	for _, key := range keys {
		delete(f.observed.Tags, key)
	}
	return nil
}

func (f *statefulKMSAPI) DeleteAlias(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	f.observed.AliasName = ""
	return nil
}

func (f *statefulKMSAPI) ScheduleKeyDeletion(context.Context, string, int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	f.observed = ObservedState{}
	return nil
}

func (f *statefulKMSAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func setupGenericKMS(t *testing.T, api KMSKeyAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := NewGenericKMSKeyDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) KMSKeyAPI { return api })
	return restatetest.Start(t, restate.Reflect(driver)).Ingress()
}

func TestGenericKMSCompositeLifecycle(t *testing.T) {
	api := &statefulKMSAPI{}
	client := setupGenericKMS(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[KMSKeySpec, KMSKeyOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~generic-kms",
		Spec: KMSKeySpec{
			Account: "test", Region: "us-east-1", Name: "generic-kms", Description: "generic pilot",
			EnableKeyRotation: true, Tags: map[string]string{"suite": "generic"},
		},
		Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs KMSKeySpec) {
			if !inputs.EnableKeyRotation {
				t.Error("generic create must converge post-create rotation")
			}
		},
	})
}

func TestGenericKMSObservedImportLifecycle(t *testing.T) {
	api := &statefulKMSAPI{observed: ObservedState{
		ARN: "arn:aws:kms:us-east-1:123456789012:key/key-existing", KeyID: "key-existing",
		AliasName: "alias/existing", Description: "existing", KeyUsage: defaultKeyUsage,
		KeySpec: defaultKeySpec, KeyState: "Enabled", Enabled: true,
		Tags: map[string]string{"suite": "generic"},
	}}
	client := setupGenericKMS(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[KMSKeyOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~existing",
		Ref: types.ImportRef{ResourceID: "existing", Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericKMSRejectsImmutableIdentityAndRetainsInputs(t *testing.T) {
	api := &statefulKMSAPI{}
	client := setupGenericKMS(t, api)
	key := "us-east-1~immutable-kms"
	spec := KMSKeySpec{Account: "test", Region: "us-east-1", Name: "immutable-kms"}
	_, err := ingress.Object[KMSKeySpec, KMSKeyOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	accepted, err := ingress.Object[restate.Void, KMSKeySpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	tests := []struct {
		field  string
		mutate func(*KMSKeySpec)
	}{
		{field: "name", mutate: func(s *KMSKeySpec) { s.Name = "different-kms" }},
		{field: "keyUsage", mutate: func(s *KMSKeySpec) { s.KeyUsage = "SIGN_VERIFY" }},
		{field: "keySpec", mutate: func(s *KMSKeySpec) { s.KeySpec = "RSA_2048" }},
	}
	for _, tt := range tests {
		changed := accepted
		tt.mutate(&changed)
		_, err = ingress.Object[KMSKeySpec, KMSKeyOutputs](client, ServiceName, key, "Provision").Request(t.Context(), changed)
		require.Error(t, err)
		assert.Contains(t, err.Error(), tt.field+" is immutable")
		retained, getErr := ingress.Object[restate.Void, KMSKeySpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
		require.NoError(t, getErr)
		assert.Equal(t, accepted, retained)
	}
}
