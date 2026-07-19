package s3

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
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type statefulS3API struct {
	mu         sync.Mutex
	observed   ObservedState
	creates    int
	reads      int
	configures int
	deletes    int
}

func (f *statefulS3API) HeadBucket(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.BucketName == "" {
		return errors.New("NoSuchBucket: missing")
	}
	return nil
}

func (f *statefulS3API) CreateBucket(_ context.Context, name, region string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	f.observed = ObservedState{
		BucketName: name, Region: region, VersioningStatus: "Suspended",
		EncryptionAlgo: "AES256", Tags: map[string]string{},
	}
	return nil
}

func (f *statefulS3API) ConfigureBucket(_ context.Context, spec S3BucketSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configures++
	f.observed.VersioningStatus = "Suspended"
	if spec.Versioning {
		f.observed.VersioningStatus = "Enabled"
	}
	if spec.Encryption.Enabled {
		f.observed.EncryptionAlgo = spec.Encryption.Algorithm
	}
	f.observed.Tags = cloneStringMap(spec.Tags)
	return nil
}

func (f *statefulS3API) DescribeBucket(_ context.Context, name string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.observed.BucketName != name {
		return ObservedState{}, errors.New("NoSuchBucket: missing")
	}
	return f.observed, nil
}

func (f *statefulS3API) DeleteBucket(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	f.observed = ObservedState{}
	return nil
}

func (f *statefulS3API) FindByTags(context.Context, map[string]string) (string, error) {
	return "", nil
}

func (f *statefulS3API) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{
		Creates: f.creates, Reads: f.reads, Updates: f.configures, Deletes: f.deletes,
	}
}

func setupGenericS3(t *testing.T, api S3API) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := NewGenericS3BucketDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) S3API { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver)).Ingress()
}

func TestGenericS3CoreLifecycleAndLateInitialization(t *testing.T) {
	api := &statefulS3API{}
	client := setupGenericS3(t, api)
	spec := S3BucketSpec{
		Account: "test", BucketName: "generic-bucket", Region: "us-east-1",
		Versioning: true, Encryption: EncryptionSpec{Enabled: true},
		Tags: map[string]string{"env": "test"},
	}
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[S3BucketSpec, S3BucketOutputs]{
		Client: client, ServiceName: ServiceName, Key: "generic-bucket", Spec: spec,
		Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs S3BucketSpec) {
			assert.Equal(t, "AES256", inputs.Encryption.Algorithm)
		},
	})
}

func TestGenericS3ObservedImportLifecycle(t *testing.T) {
	api := &statefulS3API{observed: ObservedState{
		BucketName: "existing", Region: "us-west-2", VersioningStatus: "Enabled",
		EncryptionAlgo: "AES256", Tags: map[string]string{"env": "prod"},
	}}
	client := setupGenericS3(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[S3BucketOutputs]{
		Client: client, ServiceName: ServiceName, Key: "existing",
		Ref: types.ImportRef{ResourceID: "existing", Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericS3RejectsImmutableIdentityAndRetainsInputs(t *testing.T) {
	api := &statefulS3API{}
	client := setupGenericS3(t, api)
	key := "immutable-bucket"
	spec := S3BucketSpec{Account: "test", BucketName: key, Region: "us-east-1", Tags: map[string]string{}}
	_, err := ingress.Object[S3BucketSpec, S3BucketOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	accepted, err := ingress.Object[restate.Void, S3BucketSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	tests := []struct {
		field  string
		mutate func(*S3BucketSpec)
	}{
		{field: "bucketName", mutate: func(s *S3BucketSpec) { s.BucketName = "different-bucket" }},
		{field: "region", mutate: func(s *S3BucketSpec) { s.Region = "us-west-2" }},
	}
	for _, tt := range tests {
		changed := accepted
		tt.mutate(&changed)
		_, err = ingress.Object[S3BucketSpec, S3BucketOutputs](client, ServiceName, key, "Provision").Request(t.Context(), changed)
		require.Error(t, err)
		assert.Contains(t, err.Error(), tt.field+" is immutable")
		retained, getErr := ingress.Object[restate.Void, S3BucketSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
		require.NoError(t, getErr)
		assert.Equal(t, accepted, retained)
	}
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	maps.Copy(output, input)
	return output
}
