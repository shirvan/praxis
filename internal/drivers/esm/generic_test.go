package esm

import (
	"context"
	"encoding/json"
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

type statefulESMAPI struct {
	mu                               sync.Mutex
	item                             *ObservedState
	creates, reads, updates, deletes int
	createState                      string
}

func (f *statefulESMAPI) CreateEventSourceMapping(_ context.Context, s EventSourceMappingSpec) (EventSourceMappingOutputs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.item != nil {
		return EventSourceMappingOutputs{}, &smithy.GenericAPIError{Code: "ResourceConflictException"}
	}
	f.creates++
	o := observedESM("uuid-1", s)
	if f.createState != "" {
		o.State = f.createState
	}
	f.item = &o
	return outputsFromObserved(o), nil
}
func (f *statefulESMAPI) GetEventSourceMapping(_ context.Context, id string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.item == nil || f.item.UUID != id {
		return ObservedState{}, esmNotFound()
	}
	return *f.item, nil
}
func (f *statefulESMAPI) FindEventSourceMapping(_ context.Context, fn, source string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.item != nil && f.item.FunctionArn == fn && f.item.EventSourceArn == source {
		return f.item.UUID, nil
	}
	return "", nil
}
func (f *statefulESMAPI) UpdateEventSourceMapping(_ context.Context, id string, s EventSourceMappingSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.item == nil || f.item.UUID != id {
		return esmNotFound()
	}
	f.updates++
	o := observedESM(id, s)
	f.item = &o
	return nil
}
func (f *statefulESMAPI) DeleteEventSourceMapping(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.item == nil || f.item.UUID != id {
		return esmNotFound()
	}
	f.deletes++
	f.item = nil
	return nil
}
func (f *statefulESMAPI) WaitForStableState(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.item == nil {
		return "Deleted", nil
	}
	return f.item.State, nil
}
func (f *statefulESMAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}
func (f *statefulESMAPI) seed(s EventSourceMappingSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := observedESM("uuid-import", s)
	f.item = &o
}
func (f *statefulESMAPI) remove() { f.mu.Lock(); defer f.mu.Unlock(); f.item = nil }
func (f *statefulESMAPI) forceBatchSize(value int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.item.BatchSize = value
}
func observedESM(id string, s EventSourceMappingSpec) ObservedState {
	state := "Disabled"
	if s.Enabled {
		state = "Enabled"
	}
	batch := int32(100)
	if s.BatchSize != nil {
		batch = *s.BatchSize
	}
	return ObservedState{UUID: id, EventSourceArn: s.EventSourceArn, FunctionArn: s.FunctionName, State: state, BatchSize: batch, StartingPosition: s.StartingPosition, FilterCriteria: s.FilterCriteria, FunctionResponseTypes: append([]string(nil), s.FunctionResponseTypes...)}
}
func esmNotFound() error {
	return &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "not found"}
}

type esmSink struct{}

func (esmSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (esmSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }
func setupGenericESM(t *testing.T, api ESMAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	d := newGenericEventSourceMappingDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) ESMAPI { return api })
	return restatetest.Start(t, restate.Reflect(d), restate.Reflect(esmSink{})).Ingress()
}
func genericESMSpec() EventSourceMappingSpec {
	batch := int32(10)
	return EventSourceMappingSpec{Account: "test", Region: "us-east-1", FunctionName: "fn", EventSourceArn: "arn:queue", Enabled: true, BatchSize: &batch, StartingPosition: "LATEST"}
}

func provisionESM(t *testing.T, client *ingress.Client, key string, spec EventSourceMappingSpec, mode types.ReconcileMode) (EventSourceMappingOutputs, error) {
	t.Helper()
	encoded, err := json.Marshal(spec)
	require.NoError(t, err)
	request := types.ProvisionRequest{Spec: encoded, Lifecycle: types.LifecyclePolicy{Reconcile: mode}}
	return ingress.Object[types.ProvisionRequest, EventSourceMappingOutputs](client, ServiceName, key, "Provision").Request(t.Context(), request)
}
func TestGenericESMCoreAndImport(t *testing.T) {
	api := &statefulESMAPI{}
	c := setupGenericESM(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[EventSourceMappingSpec, EventSourceMappingOutputs]{Client: c, ServiceName: ServiceName, Key: "us-east-1~esm", Spec: genericESMSpec(), Snapshot: api.snapshot, AssertInputs: func(t *testing.T, s EventSourceMappingSpec) { assert.Equal(t, "us-east-1~esm", s.ManagedKey) }})
	api = &statefulESMAPI{}
	api.seed(genericESMSpec())
	c = setupGenericESM(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[EventSourceMappingOutputs]{Client: c, ServiceName: ServiceName, Key: "us-east-1~import", Ref: types.ImportRef{ResourceID: "uuid-import", Account: "test"}, Snapshot: api.snapshot})
}
func TestGenericESMProvisionChangeAndExternalDelete(t *testing.T) {
	api := &statefulESMAPI{}
	c := setupGenericESM(t, api)
	key := "us-east-1~change"
	s := genericESMSpec()
	_, err := provisionESM(t, c, key, s, types.ReconcileModeAuto)
	require.NoError(t, err)
	stamp := "2026-01-01T00:00:00Z"
	s.StartingPosition = "AT_TIMESTAMP"
	s.StartingPositionTimestamp = &stamp
	_, err = provisionESM(t, c, key, s, types.ReconcileModeAuto)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "immutable")
	api = &statefulESMAPI{}
	c = setupGenericESM(t, api)
	_, err = provisionESM(t, c, "us-east-1~gone", genericESMSpec(), types.ReconcileModeAuto)
	require.NoError(t, err)
	api.remove()
	r, err := ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, "us-east-1~gone", "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, r.ReplacementRequired)
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericESMManagedDriftCorrectionAndObservePolicy(t *testing.T) {
	api := &statefulESMAPI{}
	c := setupGenericESM(t, api)
	key := "us-east-1~managed"
	_, err := provisionESM(t, c, key, genericESMSpec(), types.ReconcileModeAuto)
	require.NoError(t, err)
	api.forceBatchSize(99)
	result, err := ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Empty(t, result.Error)
	assert.Equal(t, 1, api.snapshot().Updates)
	status, err := ingress.Object[restate.Void, types.StatusResponse](c, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Empty(t, status.Error)

	api = &statefulESMAPI{}
	c = setupGenericESM(t, api)
	key = "us-east-1~observe"
	_, err = provisionESM(t, c, key, genericESMSpec(), types.ReconcileModeObserve)
	require.NoError(t, err)
	api.forceBatchSize(99)
	result, err = ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.False(t, result.Correcting)
	assert.Zero(t, api.snapshot().Updates)
}

func TestGenericESMReadinessPendingAndFailed(t *testing.T) {
	api := &statefulESMAPI{createState: "Creating"}
	c := setupGenericESM(t, api)
	_, err := provisionESM(t, c, "us-east-1~pending", genericESMSpec(), types.ReconcileModeAuto)
	require.NoError(t, err)
	status, err := ingress.Object[restate.Void, types.StatusResponse](c, ServiceName, "us-east-1~pending", "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusPending, status.Status)

	api = &statefulESMAPI{createState: "Failed"}
	c = setupGenericESM(t, api)
	_, err = provisionESM(t, c, "us-east-1~failed", genericESMSpec(), types.ReconcileModeAuto)
	require.Error(t, err)
}
