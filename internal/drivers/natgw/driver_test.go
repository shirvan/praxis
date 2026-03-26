package natgw

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"

	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"
	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/eventing"

	"github.com/shirvan/praxis/pkg/types"
)

const natGatewayDriftRecorderObjectServiceName = "NATGatewayTestDriftRecorder"

type natGatewayDriftRecorder struct{}

func (natGatewayDriftRecorder) ServiceName() string {
	return eventing.ResourceEventBridgeServiceName
}

func (natGatewayDriftRecorder) ReportDrift(ctx restate.Context, req eventing.DriftReportRequest) error {
	_, err := restate.WithRequestType[eventing.DriftReportRequest, restate.Void](
		restate.Object[restate.Void](ctx, natGatewayDriftRecorderObjectServiceName, req.ResourceKey, "Append"),
	).Request(req)
	return err
}

type natGatewayDriftRecorderObject struct{}

func (natGatewayDriftRecorderObject) ServiceName() string {
	return natGatewayDriftRecorderObjectServiceName
}

func (natGatewayDriftRecorderObject) Append(ctx restate.ObjectContext, req eventing.DriftReportRequest) error {
	reports, err := restate.Get[[]eventing.DriftReportRequest](ctx, "reports")
	if err != nil {
		return err
	}
	reports = append(reports, req)
	restate.Set(ctx, "reports", reports)
	return nil
}

func (natGatewayDriftRecorderObject) List(ctx restate.ObjectSharedContext, _ restate.Void) ([]eventing.DriftReportRequest, error) {
	reports, err := restate.Get[[]eventing.DriftReportRequest](ctx, "reports")
	if err != nil || reports == nil {
		return nil, err
	}
	return reports, nil
}

type fakeNATGatewayAPI struct {
	mu sync.Mutex

	nextID             string
	observed           map[string]ObservedState
	managedKeys        map[string]string
	createCalls        int
	updateCalls        int
	deleteCalls        int
	waitAvailableCalls int
	waitDeletedCalls   int

	createFunc        func(context.Context, NATGatewaySpec) (string, error)
	describeFunc      func(context.Context, string) (ObservedState, error)
	deleteFunc        func(context.Context, string) error
	waitAvailableFunc func(context.Context, string) error
	waitDeletedFunc   func(context.Context, string) error
	updateFunc        func(context.Context, string, map[string]string) error
	findFunc          func(context.Context, string) (string, error)
}

func newFakeNATGatewayAPI() *fakeNATGatewayAPI {
	return &fakeNATGatewayAPI{
		nextID:      "nat-123",
		observed:    map[string]ObservedState{},
		managedKeys: map[string]string{},
	}
}

func (f *fakeNATGatewayAPI) CreateNATGateway(ctx context.Context, spec NATGatewaySpec) (string, error) {
	if f.createFunc != nil {
		return f.createFunc(ctx, spec)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	id := f.nextID
	if id == "" {
		id = fmt.Sprintf("nat-%d", f.createCalls)
	}
	tags := map[string]string{"praxis:managed-key": spec.ManagedKey}
	maps.Copy(tags, spec.Tags)
	obs := ObservedState{
		NatGatewayId:       id,
		SubnetId:           spec.SubnetId,
		VpcId:              "vpc-123",
		ConnectivityType:   spec.ConnectivityType,
		State:              "pending",
		AllocationId:       spec.AllocationId,
		PublicIp:           "203.0.113.10",
		PrivateIp:          "10.0.1.10",
		NetworkInterfaceId: "eni-123",
		Tags:               tags,
	}
	if spec.ConnectivityType == "private" {
		obs.PublicIp = ""
		obs.AllocationId = ""
	}
	f.observed[id] = obs
	if spec.ManagedKey != "" {
		f.managedKeys[spec.ManagedKey] = id
	}
	return id, nil
}

func (f *fakeNATGatewayAPI) DescribeNATGateway(ctx context.Context, natGatewayId string) (ObservedState, error) {
	if f.describeFunc != nil {
		return f.describeFunc(ctx, natGatewayId)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obs, ok := f.observed[natGatewayId]
	if !ok {
		return ObservedState{}, &mockAPIError{code: "NatGatewayNotFound", message: "missing"}
	}
	return cloneObservedState(obs), nil
}

func (f *fakeNATGatewayAPI) DeleteNATGateway(ctx context.Context, natGatewayId string) error {
	if f.deleteFunc != nil {
		return f.deleteFunc(ctx, natGatewayId)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	obs, ok := f.observed[natGatewayId]
	if !ok {
		return &mockAPIError{code: "NatGatewayNotFound", message: "missing"}
	}
	obs.State = "deleting"
	f.observed[natGatewayId] = obs
	return nil
}

func (f *fakeNATGatewayAPI) WaitUntilAvailable(ctx context.Context, natGatewayId string) error {
	if f.waitAvailableFunc != nil {
		return f.waitAvailableFunc(ctx, natGatewayId)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waitAvailableCalls++
	obs, ok := f.observed[natGatewayId]
	if !ok {
		return &mockAPIError{code: "NatGatewayNotFound", message: "missing"}
	}
	if obs.State == "pending" {
		obs.State = "available"
		f.observed[natGatewayId] = obs
	}
	return nil
}

func (f *fakeNATGatewayAPI) WaitUntilDeleted(ctx context.Context, natGatewayId string) error {
	if f.waitDeletedFunc != nil {
		return f.waitDeletedFunc(ctx, natGatewayId)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waitDeletedCalls++
	if _, ok := f.observed[natGatewayId]; !ok {
		return &mockAPIError{code: "NatGatewayNotFound", message: "missing"}
	}
	delete(f.observed, natGatewayId)
	for key, value := range f.managedKeys {
		if value == natGatewayId {
			delete(f.managedKeys, key)
		}
	}
	return nil
}

func (f *fakeNATGatewayAPI) UpdateTags(ctx context.Context, natGatewayId string, tags map[string]string) error {
	if f.updateFunc != nil {
		return f.updateFunc(ctx, natGatewayId, tags)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	obs, ok := f.observed[natGatewayId]
	if !ok {
		return &mockAPIError{code: "NatGatewayNotFound", message: "missing"}
	}
	praxisTags := map[string]string{}
	for key, value := range obs.Tags {
		if len(key) >= 7 && key[:7] == "praxis:" {
			praxisTags[key] = value
		}
	}
	obs.Tags = map[string]string{}
	maps.Copy(obs.Tags, praxisTags)
	maps.Copy(obs.Tags, tags)
	f.observed[natGatewayId] = obs
	return nil
}

func (f *fakeNATGatewayAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	if f.findFunc != nil {
		return f.findFunc(ctx, managedKey)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.managedKeys[managedKey], nil
}

func cloneObservedState(obs ObservedState) ObservedState {
	clone := obs
	if obs.Tags != nil {
		clone.Tags = make(map[string]string, len(obs.Tags))
		maps.Copy(clone.Tags, obs.Tags)
	}
	return clone
}

func setupNATGatewayDriver(t *testing.T, api NATGatewayAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")

	driver := NewNATGatewayDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(cfg aws.Config) NATGatewayAPI {
		return api
	})
	env := restatetest.Start(t,
		restate.Reflect(driver),
		restate.Reflect(natGatewayDriftRecorder{}),
		restate.Reflect(natGatewayDriftRecorderObject{}),
	)
	return env.Ingress()
}

func pollNATGatewayEventTypes(t *testing.T, client *ingress.Client, streamKey string, expected ...string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		records, err := ingress.Object[restate.Void, []eventing.DriftReportRequest](client, natGatewayDriftRecorderObjectServiceName, streamKey, "List").Request(t.Context(), restate.Void{})
		require.NoError(t, err)
		typesSeen := make([]string, 0, len(records))
		seen := make(map[string]bool, len(records))
		for _, record := range records {
			typesSeen = append(typesSeen, record.EventType)
			seen[record.EventType] = true
		}
		complete := true
		for _, want := range expected {
			if !seen[want] {
				complete = false
				break
			}
		}
		if complete || time.Now().After(deadline) {
			return typesSeen
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func publicSpec(key string, tags map[string]string) NATGatewaySpec {
	if tags == nil {
		tags = map[string]string{"Name": "nat-a"}
	}
	return NATGatewaySpec{
		Account:          "test",
		Region:           "us-east-1",
		SubnetId:         "subnet-123",
		ConnectivityType: "public",
		AllocationId:     "eipalloc-123",
		ManagedKey:       key,
		Tags:             tags,
	}
}

func privateSpec(key string, tags map[string]string) NATGatewaySpec {
	if tags == nil {
		tags = map[string]string{"Name": "nat-a"}
	}
	return NATGatewaySpec{
		Account:          "test",
		Region:           "us-east-1",
		SubnetId:         "subnet-123",
		ConnectivityType: "private",
		ManagedKey:       key,
		Tags:             tags,
	}
}

func TestServiceName(t *testing.T) {
	drv := NewNATGatewayDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestProvision_CreatesPublicNATGW(t *testing.T) {
	api := newFakeNATGatewayAPI()
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-a"

	outputs, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), publicSpec(key, map[string]string{"Name": "nat-a", "env": "dev"}))
	require.NoError(t, err)
	assert.Equal(t, "nat-123", outputs.NatGatewayId)
	assert.Equal(t, "subnet-123", outputs.SubnetId)
	assert.Equal(t, "public", outputs.ConnectivityType)
	assert.Equal(t, "eipalloc-123", outputs.AllocationId)
	assert.Equal(t, 1, api.createCalls)
	assert.Equal(t, 1, api.waitAvailableCalls)
}

func TestProvision_CreatesPrivateNATGW(t *testing.T) {
	api := newFakeNATGatewayAPI()
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-private"

	outputs, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), privateSpec(key, nil))
	require.NoError(t, err)
	assert.Equal(t, "private", outputs.ConnectivityType)
	assert.Empty(t, outputs.AllocationId)
	assert.Empty(t, outputs.PublicIp)
}

func TestProvision_MissingSubnetIdFails(t *testing.T) {
	api := newFakeNATGatewayAPI()
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-a"

	_, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), NATGatewaySpec{Account: "test", Region: "us-east-1", ManagedKey: key})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subnetId is required")
}

func TestProvision_PublicMissingAllocationIdFails(t *testing.T) {
	api := newFakeNATGatewayAPI()
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-a"

	_, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), NATGatewaySpec{Account: "test", Region: "us-east-1", SubnetId: "subnet-123", ManagedKey: key})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allocationId is required")
}

func TestProvision_PrivateWithAllocationIdFails(t *testing.T) {
	api := newFakeNATGatewayAPI()
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-a"

	_, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), NATGatewaySpec{Account: "test", Region: "us-east-1", SubnetId: "subnet-123", ConnectivityType: "private", AllocationId: "eipalloc-123", ManagedKey: key})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be empty for private NAT gateways")
}

func TestProvision_IdempotentReprovision(t *testing.T) {
	api := newFakeNATGatewayAPI()
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-a"
	spec := publicSpec(key, map[string]string{"Name": "nat-a"})

	out1, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	out2, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.NatGatewayId, out2.NatGatewayId)
	assert.Equal(t, 1, api.createCalls)
	assert.Equal(t, 0, api.updateCalls)
}

func TestProvision_TagUpdate(t *testing.T) {
	api := newFakeNATGatewayAPI()
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-a"

	_, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), publicSpec(key, map[string]string{"Name": "nat-a", "env": "dev"}))
	require.NoError(t, err)

	_, err = ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), publicSpec(key, map[string]string{"Name": "nat-a", "env": "prod"}))
	require.NoError(t, err)
	assert.Equal(t, 1, api.updateCalls)
	assert.Equal(t, "prod", api.observed["nat-123"].Tags["env"])
}

func TestProvision_ConflictFails(t *testing.T) {
	api := newFakeNATGatewayAPI()
	api.managedKeys["us-east-1~nat-a"] = "nat-existing"
	client := setupNATGatewayDriver(t, api)

	_, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, "us-east-1~nat-a", "Provision").Request(t.Context(), publicSpec("us-east-1~nat-a", nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already managed by Praxis")
	assert.Equal(t, 0, api.createCalls)
}

func TestProvision_FailedStateRecovery(t *testing.T) {
	api := newFakeNATGatewayAPI()
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-a"

	_, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), publicSpec(key, nil))
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.observed["nat-123"]
	obs.State = "failed"
	obs.FailureCode = "Resource.AlreadyAssociated"
	obs.FailureMessage = "allocation already in use"
	api.observed["nat-123"] = obs
	api.mu.Unlock()

	_, err = ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), publicSpec(key, nil))
	require.NoError(t, err)
	assert.Equal(t, 1, api.deleteCalls)
	assert.Equal(t, 1, api.waitDeletedCalls)
	assert.Equal(t, 2, api.createCalls)
}

func TestProvision_WaitsForAvailable(t *testing.T) {
	api := newFakeNATGatewayAPI()
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-a"

	_, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), publicSpec(key, nil))
	require.NoError(t, err)
	assert.Equal(t, 1, api.waitAvailableCalls)
}

func TestImport_ExistingNATGW(t *testing.T) {
	api := newFakeNATGatewayAPI()
	api.observed["nat-123"] = ObservedState{
		NatGatewayId:       "nat-123",
		SubnetId:           "subnet-123",
		VpcId:              "vpc-123",
		ConnectivityType:   "public",
		State:              "available",
		PublicIp:           "203.0.113.10",
		PrivateIp:          "10.0.1.10",
		AllocationId:       "eipalloc-123",
		NetworkInterfaceId: "eni-123",
		Tags:               map[string]string{"Name": "nat-a", "praxis:managed-key": "us-east-1~nat-a"},
	}
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-123"

	outputs, err := ingress.Object[types.ImportRef, NATGatewayOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "nat-123", Account: "test"})
	require.NoError(t, err)
	assert.Equal(t, "nat-123", outputs.NatGatewayId)
	assert.Equal(t, "public", outputs.ConnectivityType)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestDelete_DeletesAndWaits(t *testing.T) {
	api := newFakeNATGatewayAPI()
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-a"

	_, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), publicSpec(key, nil))
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, 1, api.deleteCalls)
	assert.Equal(t, 1, api.waitDeletedCalls)
	_, ok := api.observed["nat-123"]
	assert.False(t, ok)
}

func TestDelete_AlreadyDeleted(t *testing.T) {
	api := newFakeNATGatewayAPI()
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-a"

	_, err := ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
}

func TestDelete_ObservedModeBlocked(t *testing.T) {
	api := newFakeNATGatewayAPI()
	api.observed["nat-123"] = ObservedState{NatGatewayId: "nat-123", SubnetId: "subnet-123", ConnectivityType: "public", State: "available", Tags: map[string]string{"Name": "nat-a"}}
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-123"

	_, err := ingress.Object[types.ImportRef, NATGatewayOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "nat-123", Account: "test"})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Observed mode")
}

func TestReconcile_DetectsTagDrift(t *testing.T) {
	api := newFakeNATGatewayAPI()
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-a"

	_, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), publicSpec(key, map[string]string{"Name": "nat-a", "env": "managed"}))
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.observed["nat-123"]
	obs.Tags["env"] = "drifted"
	api.observed["nat-123"] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Equal(t, "managed", api.observed["nat-123"].Tags["env"])
	assert.ElementsMatch(t, []string{eventing.DriftEventDetected, eventing.DriftEventCorrected}, pollNATGatewayEventTypes(t, client, key, eventing.DriftEventDetected, eventing.DriftEventCorrected))
}

func TestReconcile_ObservedModeReportsOnly(t *testing.T) {
	api := newFakeNATGatewayAPI()
	api.observed["nat-123"] = ObservedState{NatGatewayId: "nat-123", SubnetId: "subnet-123", ConnectivityType: "public", State: "available", Tags: map[string]string{"Name": "nat-a", "env": "managed"}}
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-123"

	_, err := ingress.Object[types.ImportRef, NATGatewayOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "nat-123", Account: "test"})
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.observed["nat-123"]
	obs.Tags["env"] = "drifted"
	api.observed["nat-123"] = obs
	api.mu.Unlock()

	state, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, state.Drift)
	assert.False(t, state.Correcting)
	assert.Contains(t, pollNATGatewayEventTypes(t, client, key, eventing.DriftEventDetected), eventing.DriftEventDetected)
}

func TestReconcile_EmitsExternalDeleteEvent(t *testing.T) {
	api := newFakeNATGatewayAPI()
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-a"

	_, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), publicSpec(key, nil))
	require.NoError(t, err)

	api.mu.Lock()
	delete(api.observed, "nat-123")
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "deleted externally")
	assert.Contains(t, pollNATGatewayEventTypes(t, client, key, eventing.DriftEventExternalDelete), eventing.DriftEventExternalDelete)
}

func TestReconcile_FailedStateReported(t *testing.T) {
	api := newFakeNATGatewayAPI()
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-a"

	_, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), publicSpec(key, nil))
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.observed["nat-123"]
	obs.State = "failed"
	obs.FailureCode = "InternalError"
	obs.FailureMessage = "creation failed"
	api.observed["nat-123"] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "failed state")

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusError, status.Status)
}

func TestGetStatusAndOutputs_ReturnCurrentState(t *testing.T) {
	api := newFakeNATGatewayAPI()
	client := setupNATGatewayDriver(t, api)
	key := "us-east-1~nat-a"

	provisioned, err := ingress.Object[NATGatewaySpec, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), publicSpec(key, nil))
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)

	outputs, err := ingress.Object[restate.Void, NATGatewayOutputs](client, ServiceName, key, "GetOutputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, provisioned.NatGatewayId, outputs.NatGatewayId)
	assert.Equal(t, provisioned.NetworkInterfaceId, outputs.NetworkInterfaceId)
}
