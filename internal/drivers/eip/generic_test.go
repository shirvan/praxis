package eip

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

type statefulEIPAPI struct {
	mu sync.Mutex

	exists   bool
	observed ObservedState
	nextID   int

	creates int
	reads   int
	updates int
	deletes int

	allocateCalls  int
	findCalls      int
	releaseCalls   int
	allocateErrors []error
	findErrors     []error
	releaseErrors  []error
	findIDOverride string
}

type eipProviderState struct {
	Exists        bool
	Observed      ObservedState
	Creates       int
	Reads         int
	Updates       int
	Deletes       int
	AllocateCalls int
	FindCalls     int
	ReleaseCalls  int
}

type eipDriftSink struct{}

func (eipDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }
func (eipDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error {
	return nil
}

func (f *statefulEIPAPI) AllocateAddress(_ context.Context, spec ElasticIPSpec) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allocateCalls++
	if f.exists {
		return "", "", errors.New("unexpected duplicate Elastic IP allocation")
	}
	f.nextID++
	id := "eipalloc-created"
	if f.nextID > 1 {
		id += "-duplicate"
	}
	tags := maps.Clone(spec.Tags)
	if tags == nil {
		tags = map[string]string{}
	}
	tags[managedKeyTag] = spec.ManagedKey
	borderGroup := spec.NetworkBorderGroup
	if borderGroup == "" {
		borderGroup = "us-east-1"
	}
	f.exists = true
	f.observed = ObservedState{
		AllocationId: id, PublicIp: "203.0.113.10", Domain: "vpc",
		NetworkBorderGroup: borderGroup, Tags: tags,
	}
	f.creates++
	if len(f.allocateErrors) > 0 {
		err := f.allocateErrors[0]
		f.allocateErrors = f.allocateErrors[1:]
		return "", "", err
	}
	return id, f.observed.PublicIp, nil
}

func (f *statefulEIPAPI) DescribeAddress(_ context.Context, allocationID string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if !f.exists || f.observed.AllocationId != allocationID {
		return ObservedState{}, awserr.NotFound("elastic IP " + allocationID + " not found")
	}
	return cloneObserved(f.observed), nil
}

func (f *statefulEIPAPI) ReleaseAddress(_ context.Context, allocationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	if len(f.releaseErrors) > 0 {
		err := f.releaseErrors[0]
		f.releaseErrors = f.releaseErrors[1:]
		return err
	}
	if !f.exists || f.observed.AllocationId != allocationID {
		return awserr.NotFound("elastic IP " + allocationID + " not found")
	}
	if f.observed.AssociationId != "" || f.observed.InstanceId != "" {
		return &mockAPIError{code: "InvalidIPAddress.InUse", message: "address is associated"}
	}
	f.exists = false
	f.observed = ObservedState{}
	f.deletes++
	return nil
}

func (f *statefulEIPAPI) UpdateTags(_ context.Context, allocationID string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.AllocationId != allocationID {
		return awserr.NotFound("elastic IP " + allocationID + " not found")
	}
	updated := maps.Clone(tags)
	if updated == nil {
		updated = map[string]string{}
	}
	for key, value := range f.observed.Tags {
		if len(key) >= len("praxis:") && key[:len("praxis:")] == "praxis:" {
			updated[key] = value
		}
	}
	f.observed.Tags = updated
	f.updates++
	return nil
}

func (f *statefulEIPAPI) FindByManagedKey(_ context.Context, managedKey string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findCalls++
	f.reads++
	if len(f.findErrors) > 0 {
		err := f.findErrors[0]
		f.findErrors = f.findErrors[1:]
		return "", err
	}
	if f.findIDOverride != "" {
		return f.findIDOverride, nil
	}
	if f.exists && f.observed.Tags[managedKeyTag] == managedKey {
		return f.observed.AllocationId, nil
	}
	return "", nil
}

func (f *statefulEIPAPI) snapshot() drivertest.ProviderSnapshot {
	state := f.current()
	return drivertest.ProviderSnapshot{Creates: state.Creates, Reads: state.Reads, Updates: state.Updates, Deletes: state.Deletes}
}

func (f *statefulEIPAPI) current() eipProviderState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return eipProviderState{
		Exists: f.exists, Observed: cloneObserved(f.observed), Creates: f.creates,
		Reads: f.reads, Updates: f.updates, Deletes: f.deletes,
		AllocateCalls: f.allocateCalls, FindCalls: f.findCalls, ReleaseCalls: f.releaseCalls,
	}
}

func (f *statefulEIPAPI) removeExternally() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exists = false
	f.observed = ObservedState{}
}

func cloneObserved(observed ObservedState) ObservedState {
	observed.Tags = maps.Clone(observed.Tags)
	return observed
}

func setupGenericEIP(t *testing.T, api EIPAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericElasticIPDriverWithFactories(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) EIPAPI { return api },
		func(restate.ObjectContext, aws.Config) (string, error) { return "123456789012", nil },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(eipDriftSink{})).Ingress()
}

func managedEIPSpec() ElasticIPSpec {
	return ElasticIPSpec{
		Account: "test", Region: "us-east-1", Tags: map[string]string{"Name": "generic-eip", "env": "test"},
	}
}

func existingEIP(allocationID, managedKey string) *statefulEIPAPI {
	return &statefulEIPAPI{exists: true, observed: ObservedState{
		AllocationId: allocationID, PublicIp: "203.0.113.20", Domain: "vpc",
		NetworkBorderGroup: "us-east-1", Tags: map[string]string{"Name": "existing", managedKeyTag: managedKey},
	}}
}

func TestGenericElasticIPServiceName(t *testing.T) {
	assert.Equal(t, ServiceName, NewGenericElasticIPDriver(nil).ServiceName())
}

func TestGenericElasticIPCoreLifecycle(t *testing.T) {
	api := &statefulEIPAPI{}
	client := setupGenericEIP(t, api)
	key := "us-east-1~generic-eip"
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[ElasticIPSpec, ElasticIPOutputs]{
		Client: client, ServiceName: ServiceName, Key: key, Spec: managedEIPSpec(), Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs ElasticIPSpec) {
			assert.Equal(t, key, inputs.ManagedKey)
			assert.Equal(t, "vpc", inputs.Domain)
			assert.Equal(t, managedEIPSpec().Tags, inputs.Tags)
		},
	})
}

func TestGenericElasticIPRejectsImmutableRegionAndRetainsInputs(t *testing.T) {
	api := &statefulEIPAPI{}
	client := setupGenericEIP(t, api)
	key := "us-east-1~immutable-eip"
	_, err := ingress.Object[ElasticIPSpec, ElasticIPOutputs](client, ServiceName, key, "Provision").Request(t.Context(), managedEIPSpec())
	require.NoError(t, err)
	accepted, err := ingress.Object[restate.Void, ElasticIPSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	changed := accepted
	changed.Region = "us-west-2"
	_, err = ingress.Object[ElasticIPSpec, ElasticIPOutputs](client, ServiceName, key, "Provision").Request(t.Context(), changed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "region is immutable")
	retained, err := ingress.Object[restate.Void, ElasticIPSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, accepted, retained)
}

func TestGenericElasticIPObservedImportLifecycle(t *testing.T) {
	api := existingEIP("eipalloc-existing", "old-owner")
	client := setupGenericEIP(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[ElasticIPOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~imported",
		Ref: types.ImportRef{ResourceID: "eipalloc-existing", Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericElasticIPRecoversAmbiguousAllocationWithoutDuplicate(t *testing.T) {
	api := &statefulEIPAPI{allocateErrors: []error{errors.New("ServiceUnavailable: allocation response lost")}}
	client := setupGenericEIP(t, api)
	outputs, err := ingress.Object[ElasticIPSpec, ElasticIPOutputs](
		client, ServiceName, "us-east-1~ambiguous", "Provision",
	).Request(t.Context(), managedEIPSpec())
	require.NoError(t, err)
	assert.Equal(t, "eipalloc-created", outputs.AllocationId)
	state := api.current()
	assert.Equal(t, 1, state.Creates)
	assert.Equal(t, 1, state.AllocateCalls)
	assert.True(t, state.Exists)
}

func TestGenericElasticIPAdoptsExactManagedKeyRecovery(t *testing.T) {
	key := "us-east-1~recover"
	api := existingEIP("eipalloc-recovered", key)
	client := setupGenericEIP(t, api)
	spec := managedEIPSpec()
	spec.Tags = map[string]string{"Name": "recovered", "env": "managed"}
	outputs, err := ingress.Object[ElasticIPSpec, ElasticIPOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, "eipalloc-recovered", outputs.AllocationId)
	state := api.current()
	assert.Zero(t, state.Creates)
	assert.Zero(t, state.AllocateCalls)
	assert.Equal(t, map[string]string{"Name": "recovered", "env": "managed", managedKeyTag: key}, state.Observed.Tags)
}

func TestGenericElasticIPRejectsInexactOwnershipRecovery(t *testing.T) {
	key := "us-east-1~wrong-owner"
	api := existingEIP("eipalloc-existing", "different-owner")
	api.findIDOverride = "eipalloc-existing"
	client := setupGenericEIP(t, api)
	_, err := ingress.Object[ElasticIPSpec, ElasticIPOutputs](client, ServiceName, key, "Provision").Request(t.Context(), managedEIPSpec())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without exact Praxis ownership tag")
	assert.Zero(t, api.current().Creates)
}

func TestGenericElasticIPFindRetriesBeforeAllocation(t *testing.T) {
	api := &statefulEIPAPI{findErrors: []error{errors.New("RequestLimitExceeded: transient lookup failure")}}
	client := setupGenericEIP(t, api)
	_, err := ingress.Object[ElasticIPSpec, ElasticIPOutputs](
		client, ServiceName, "us-east-1~find-retry", "Provision",
	).Request(t.Context(), managedEIPSpec())
	require.NoError(t, err)
	state := api.current()
	assert.GreaterOrEqual(t, state.FindCalls, 2)
	assert.Equal(t, 1, state.Creates)
}

func TestGenericElasticIPDeleteRetriesTransientRelease(t *testing.T) {
	api := &statefulEIPAPI{releaseErrors: []error{errors.New("RequestLimitExceeded: transient release failure")}}
	client := setupGenericEIP(t, api)
	key := "us-east-1~delete-retry"
	_, err := ingress.Object[ElasticIPSpec, ElasticIPOutputs](client, ServiceName, key, "Provision").Request(t.Context(), managedEIPSpec())
	require.NoError(t, err)
	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, 2, api.current().ReleaseCalls)
}

func TestGenericElasticIPDeleteRejectsAssociatedAddress(t *testing.T) {
	api := &statefulEIPAPI{}
	client := setupGenericEIP(t, api)
	key := "us-east-1~associated"
	_, err := ingress.Object[ElasticIPSpec, ElasticIPOutputs](client, ServiceName, key, "Provision").Request(t.Context(), managedEIPSpec())
	require.NoError(t, err)
	api.mu.Lock()
	api.observed.AssociationId = "eipassoc-123"
	api.observed.InstanceId = "i-123"
	api.mu.Unlock()
	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disassociate it before releasing")
	assert.True(t, api.current().Exists)
}

func TestGenericElasticIPManagedReconcileCorrectsTags(t *testing.T) {
	api := &statefulEIPAPI{}
	client := setupGenericEIP(t, api)
	key := "us-east-1~drift"
	_, err := ingress.Object[ElasticIPSpec, ElasticIPOutputs](client, ServiceName, key, "Provision").Request(t.Context(), managedEIPSpec())
	require.NoError(t, err)
	api.mu.Lock()
	api.observed.Tags = map[string]string{"Name": "drift", "stale": "remove", managedKeyTag: key}
	api.mu.Unlock()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Equal(t, map[string]string{"Name": "generic-eip", "env": "test", managedKeyTag: key}, api.current().Observed.Tags)
}

func TestGenericElasticIPExternalDeleteRequiresReplacementWithoutCreate(t *testing.T) {
	api := &statefulEIPAPI{}
	client := setupGenericEIP(t, api)
	key := "us-east-1~external-delete"
	_, err := ingress.Object[ElasticIPSpec, ElasticIPOutputs](client, ServiceName, key, "Provision").Request(t.Context(), managedEIPSpec())
	require.NoError(t, err)
	before := api.current()
	api.removeExternally()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "deleted externally")
	assert.True(t, result.ReplacementRequired)
	assert.Equal(t, before.Creates, api.current().Creates)
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusError, status.Status)
}

func TestGenericElasticIPFiltersUserOwnershipTag(t *testing.T) {
	api := &statefulEIPAPI{}
	client := setupGenericEIP(t, api)
	key := "us-east-1~authoritative"
	spec := managedEIPSpec()
	spec.Tags[managedKeyTag] = "user-supplied"
	_, err := ingress.Object[ElasticIPSpec, ElasticIPOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, key, api.current().Observed.Tags[managedKeyTag])
	inputs, err := ingress.Object[restate.Void, ElasticIPSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.NotContains(t, inputs.Tags, managedKeyTag)
}

func TestSpecFromObservedRoundTrip(t *testing.T) {
	observed := ObservedState{
		AllocationId: "eipalloc-123", PublicIp: "203.0.113.10", Domain: "vpc",
		NetworkBorderGroup: "us-east-1", Tags: map[string]string{"Name": "web", "env": "dev", managedKeyTag: "owner"},
	}
	spec := specFromObserved(observed)
	assert.Equal(t, "vpc", spec.Domain)
	assert.Equal(t, "us-east-1", spec.NetworkBorderGroup)
	assert.Equal(t, map[string]string{"Name": "web", "env": "dev"}, spec.Tags)
}

func TestOutputsFromObserved(t *testing.T) {
	outputs := outputsFromObserved(ObservedState{
		AllocationId: "eipalloc-123", PublicIp: "203.0.113.10", Domain: "vpc",
		NetworkBorderGroup: "us-east-1", Region: "us-east-1", AccountId: "123456789012",
	}, ElasticIPOutputs{})
	assert.Equal(t, "eipalloc-123", outputs.AllocationId)
	assert.Equal(t, "203.0.113.10", outputs.PublicIp)
	assert.Equal(t, "arn:aws:ec2:us-east-1:123456789012:elastic-ip/eipalloc-123", outputs.ARN)
}
