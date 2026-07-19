package ebs

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
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	"github.com/shirvan/praxis/internal/eventing"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type ebsDriftSink struct{}

func (ebsDriftSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (ebsDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

type statefulEBSAPI struct {
	mu sync.Mutex

	exists   bool
	observed ObservedState
	creates  int
	reads    int
	updates  int
	deletes  int
	nextID   int

	createErrors []error
}

func (f *statefulEBSAPI) CreateVolume(_ context.Context, spec EBSVolumeSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := "vol-created"
	if f.nextID > 1 {
		id = "vol-duplicate"
	}
	tags := maps.Clone(spec.Tags)
	if tags == nil {
		tags = map[string]string{}
	}
	tags[managedKeyTag] = spec.ManagedKey
	f.exists = true
	f.creates++
	f.observed = ObservedState{
		VolumeId: id, AvailabilityZone: spec.AvailabilityZone, VolumeType: spec.VolumeType,
		SizeGiB: spec.SizeGiB, Iops: spec.Iops, Throughput: spec.Throughput,
		Encrypted: spec.Encrypted, KmsKeyId: spec.KmsKeyId, SnapshotId: spec.SnapshotId,
		State: "available", Tags: tags,
	}
	if len(f.createErrors) > 0 {
		err := f.createErrors[0]
		f.createErrors = f.createErrors[1:]
		return "", err
	}
	return id, nil
}

func (f *statefulEBSAPI) DescribeVolume(_ context.Context, id string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if !f.exists || f.observed.VolumeId != id {
		return ObservedState{}, awserr.NotFound("volume not found")
	}
	return cloneEBSObserved(f.observed), nil
}

func (f *statefulEBSAPI) DeleteVolume(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.VolumeId != id {
		return awserr.NotFound("volume not found")
	}
	f.exists = false
	f.observed = ObservedState{}
	f.deletes++
	return nil
}

func (f *statefulEBSAPI) ModifyVolume(_ context.Context, id string, spec EBSVolumeSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.VolumeId != id {
		return awserr.NotFound("volume not found")
	}
	f.observed.VolumeType = spec.VolumeType
	f.observed.SizeGiB = spec.SizeGiB
	f.observed.Iops = spec.Iops
	f.observed.Throughput = spec.Throughput
	f.updates++
	return nil
}

func (f *statefulEBSAPI) WaitUntilAvailable(context.Context, string) error { return nil }

func (f *statefulEBSAPI) UpdateTags(_ context.Context, id string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.VolumeId != id {
		return awserr.NotFound("volume not found")
	}
	owner := f.observed.Tags[managedKeyTag]
	f.observed.Tags = maps.Clone(tags)
	f.observed.Tags[managedKeyTag] = owner
	f.updates++
	return nil
}

func (f *statefulEBSAPI) FindByManagedKey(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.exists && f.observed.Tags[managedKeyTag] == key {
		return f.observed.VolumeId, nil
	}
	return "", nil
}

func (f *statefulEBSAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulEBSAPI) removeExternally() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exists = false
	f.observed = ObservedState{}
}

func cloneEBSObserved(in ObservedState) ObservedState {
	out := in
	out.Tags = maps.Clone(in.Tags)
	return out
}

func setupGenericEBS(t *testing.T, api EBSAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericEBSVolumeDriverWithFactories(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) EBSAPI { return api },
		func(restate.ObjectContext, aws.Config) (string, error) { return "123456789012", nil },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(ebsDriftSink{})).Ingress()
}

func managedEBSSpec() EBSVolumeSpec {
	return EBSVolumeSpec{
		Account: "test", Region: "us-east-1", AvailabilityZone: "us-east-1a",
		VolumeType: "gp3", SizeGiB: 20, Encrypted: true, Tags: map[string]string{"env": "test"},
	}
}

func TestGenericEBSCoreLifecycle(t *testing.T) {
	api := &statefulEBSAPI{}
	client := setupGenericEBS(t, api)
	key := "us-east-1~generic-ebs"
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[EBSVolumeSpec, EBSVolumeOutputs]{
		Client: client, ServiceName: ServiceName, Key: key, Spec: managedEBSSpec(), Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs EBSVolumeSpec) {
			assert.Equal(t, key, inputs.ManagedKey)
			assert.Equal(t, managedEBSSpec().Tags, inputs.Tags)
		},
	})
}

func TestGenericEBSObservedImportLifecycle(t *testing.T) {
	api := &statefulEBSAPI{exists: true, observed: ObservedState{
		VolumeId: "vol-existing", AvailabilityZone: "us-east-1a", VolumeType: "gp3",
		SizeGiB: 20, Encrypted: true, State: "available", Tags: map[string]string{"env": "import"},
	}}
	client := setupGenericEBS(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[EBSVolumeOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~vol-existing",
		Ref: types.ImportRef{Account: "test", ResourceID: "vol-existing"}, Snapshot: api.snapshot,
	})
}

func TestGenericEBSRecoversAmbiguousCreate(t *testing.T) {
	api := &statefulEBSAPI{createErrors: []error{errors.New("request timeout")}}
	client := setupGenericEBS(t, api)
	_, err := ingress.Object[types.ProvisionRequest, EBSVolumeOutputs](client, ServiceName, "us-east-1~generic-ebs", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedEBSSpec()))
	require.NoError(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericEBSRejectsImmutableAZ(t *testing.T) {
	api := &statefulEBSAPI{}
	client := setupGenericEBS(t, api)
	key := "us-east-1~generic-ebs"
	spec := managedEBSSpec()
	_, err := ingress.Object[types.ProvisionRequest, EBSVolumeOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	spec.AvailabilityZone = "us-east-1b"
	_, err = ingress.Object[types.ProvisionRequest, EBSVolumeOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "availabilityZone is immutable")
}

func TestGenericEBSExternalDeleteRequiresReplacement(t *testing.T) {
	api := &statefulEBSAPI{}
	client := setupGenericEBS(t, api)
	key := "us-east-1~generic-ebs"
	_, err := ingress.Object[types.ProvisionRequest, EBSVolumeOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedEBSSpec()))
	require.NoError(t, err)
	before := api.snapshot()
	api.removeExternally()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "deleted externally")
	assert.Equal(t, before.Creates, api.snapshot().Creates)
}
