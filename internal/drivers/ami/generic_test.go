package ami

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	"github.com/shirvan/praxis/internal/eventing"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maps"
	"sync"
	"testing"
	"time"
)

type statefulAMIAPI struct {
	mu                               sync.Mutex
	item                             *ObservedState
	creates, reads, updates, deletes int
	createState                      string
}

func (f *statefulAMIAPI) RegisterImage(_ context.Context, s AMISpec) (string, error) {
	return f.create(s)
}
func (f *statefulAMIAPI) CopyImage(_ context.Context, s AMISpec) (string, error) { return f.create(s) }
func (f *statefulAMIAPI) create(s AMISpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.item != nil {
		return "", &smithy.GenericAPIError{Code: "InvalidAMIName.Duplicate"}
	}
	f.creates++
	o := observedAMI(s)
	if f.createState != "" {
		o.State = f.createState
	}
	f.item = &o
	return o.ImageId, nil
}
func (f *statefulAMIAPI) DescribeImage(_ context.Context, id string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.item == nil || f.item.ImageId != id {
		return ObservedState{}, amiNotFound()
	}
	o := *f.item
	o.Tags = maps.Clone(o.Tags)
	return o, nil
}
func (f *statefulAMIAPI) DescribeImageByName(_ context.Context, name string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.item == nil || f.item.Name != name {
		return ObservedState{}, amiNotFound()
	}
	return *f.item, nil
}
func (f *statefulAMIAPI) DeregisterImage(_ context.Context, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.item == nil {
		return amiNotFound()
	}
	f.deletes++
	f.item = nil
	return nil
}
func (f *statefulAMIAPI) UpdateTags(_ context.Context, _ string, tags map[string]string) error {
	return f.mutate(func(o *ObservedState) { o.Tags = maps.Clone(tags) })
}
func (f *statefulAMIAPI) ModifyDescription(_ context.Context, _ string, v string) error {
	return f.mutate(func(o *ObservedState) { o.Description = v })
}
func (f *statefulAMIAPI) ModifyLaunchPermissions(_ context.Context, _ string, p *LaunchPermsSpec) error {
	return f.mutate(func(o *ObservedState) {
		n := normalizeLaunchPermSpec(p)
		o.LaunchPermPublic = n.Public
		o.LaunchPermAccounts = n.AccountIds
	})
}
func (f *statefulAMIAPI) EnableDeprecation(_ context.Context, _ string, v string) error {
	return f.mutate(func(o *ObservedState) { o.DeprecationTime = v })
}
func (f *statefulAMIAPI) DisableDeprecation(_ context.Context, _ string) error {
	return f.mutate(func(o *ObservedState) { o.DeprecationTime = "" })
}
func (f *statefulAMIAPI) WaitUntilAvailable(context.Context, string, time.Duration) error { return nil }
func (f *statefulAMIAPI) FindByManagedKey(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.item != nil && f.item.Tags["praxis:managed-key"] == key {
		return f.item.ImageId, nil
	}
	return "", nil
}
func (f *statefulAMIAPI) mutate(fn func(*ObservedState)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.item == nil {
		return amiNotFound()
	}
	f.updates++
	fn(f.item)
	return nil
}
func (f *statefulAMIAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}
func (f *statefulAMIAPI) seed(s AMISpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := observedAMI(s)
	f.item = &o
}
func (f *statefulAMIAPI) remove() { f.mu.Lock(); defer f.mu.Unlock(); f.item = nil }
func (f *statefulAMIAPI) forceState(state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.item.State = state
}
func observedAMI(s AMISpec) ObservedState {
	arch := "x86_64"
	virt := "hvm"
	root := "/dev/sda1"
	if s.Source.FromSnapshot != nil {
		arch = s.Source.FromSnapshot.Architecture
		virt = s.Source.FromSnapshot.VirtualizationType
		root = s.Source.FromSnapshot.RootDeviceName
	}
	return ObservedState{ImageId: "ami-1", Name: s.Name, Description: s.Description, State: "available", Architecture: arch, VirtualizationType: virt, RootDeviceName: root, Tags: desiredTags(s)}
}
func amiNotFound() error {
	return &smithy.GenericAPIError{Code: "InvalidAMIID.NotFound", Message: "not found"}
}

func TestClassifyAMIMutationTerminalizesInvalidSourceAndPreservesTerminalErrors(t *testing.T) {
	classified := classifyAMIMutation(amiNotFound())
	require.True(t, restate.IsTerminalError(classified))
	assert.Equal(t, restate.Code(400), restate.ErrorCode(classified))

	alreadyTerminal := restate.TerminalError(errors.New("conflict"), 409)
	assert.Same(t, alreadyTerminal, classifyAMIMutation(alreadyTerminal))
}

type amiSink struct{}

func (amiSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (amiSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }
func setupGenericAMI(t *testing.T, api AMIAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	d := newGenericAMIDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) AMIAPI { return api })
	return restatetest.Start(t, restate.Reflect(d), restate.Reflect(amiSink{})).Ingress()
}
func genericAMISpec() AMISpec {
	return AMISpec{Account: "test", Region: "us-east-1", Name: "base", Description: "v1", Source: SourceSpec{FromSnapshot: &FromSnapshotSpec{SnapshotId: "snap-1", Architecture: "x86_64", VirtualizationType: "hvm", RootDeviceName: "/dev/sda1", VolumeType: "gp3"}}, Tags: map[string]string{"env": "test"}}
}
func TestGenericAMICoreAndImport(t *testing.T) {
	api := &statefulAMIAPI{}
	c := setupGenericAMI(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[AMISpec, AMIOutputs]{Client: c, ServiceName: ServiceName, Key: "us-east-1~ami", Spec: genericAMISpec(), Snapshot: api.snapshot, AssertInputs: func(t *testing.T, s AMISpec) { assert.Equal(t, "us-east-1~ami", s.ManagedKey) }})
	api = &statefulAMIAPI{}
	s := genericAMISpec()
	s.ManagedKey = "other"
	api.seed(s)
	c = setupGenericAMI(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[AMIOutputs]{Client: c, ServiceName: ServiceName, Key: "us-east-1~import", Ref: types.ImportRef{ResourceID: "ami-1", Account: "test"}, Snapshot: api.snapshot})
	_, err := ingress.Object[types.ProvisionRequest, AMIOutputs](c, ServiceName, "us-east-1~import", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, genericAMISpec()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "immutable")
}
func TestGenericAMIOpaqueImmutableDriftAndExternalDelete(t *testing.T) {
	api := &statefulAMIAPI{}
	c := setupGenericAMI(t, api)
	key := "us-east-1~ami"
	s := genericAMISpec()
	_, err := ingress.Object[types.ProvisionRequest, AMIOutputs](c, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, s))
	require.NoError(t, err)
	s.Description = "v2"
	_, err = ingress.Object[types.ProvisionRequest, AMIOutputs](c, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, s))
	require.NoError(t, err)
	assert.Greater(t, api.snapshot().Updates, 0)
	s.Source.FromSnapshot.SnapshotId = "snap-2"
	_, err = ingress.Object[types.ProvisionRequest, AMIOutputs](c, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "immutable")
	api = &statefulAMIAPI{}
	c = setupGenericAMI(t, api)
	_, err = ingress.Object[types.ProvisionRequest, AMIOutputs](c, ServiceName, "us-east-1~gone", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, genericAMISpec()))
	require.NoError(t, err)
	api.remove()
	r, err := ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, "us-east-1~gone", "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, r.ReplacementRequired)
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericAMIReadinessPendingAndFailed(t *testing.T) {
	api := &statefulAMIAPI{createState: "pending"}
	c := setupGenericAMI(t, api)
	key := "us-east-1~pending"
	_, err := ingress.Object[types.ProvisionRequest, AMIOutputs](c, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, genericAMISpec()))
	require.NoError(t, err)
	status, err := ingress.Object[restate.Void, types.StatusResponse](c, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusPending, status.Status)
	api.forceState("available")
	_, err = ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	status, err = ingress.Object[restate.Void, types.StatusResponse](c, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)

	api = &statefulAMIAPI{createState: "failed"}
	c = setupGenericAMI(t, api)
	_, err = ingress.Object[types.ProvisionRequest, AMIOutputs](c, ServiceName, "us-east-1~failed", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, genericAMISpec()))
	require.Error(t, err)
}
