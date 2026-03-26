package igw

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

const igwDriftRecorderObjectServiceName = "IGWTestDriftRecorder"

type igwDriftRecorder struct{}

func (igwDriftRecorder) ServiceName() string {
	return eventing.ResourceEventBridgeServiceName
}

func (igwDriftRecorder) ReportDrift(ctx restate.Context, req eventing.DriftReportRequest) error {
	_, err := restate.WithRequestType[eventing.DriftReportRequest, restate.Void](
		restate.Object[restate.Void](ctx, igwDriftRecorderObjectServiceName, req.ResourceKey, "Append"),
	).Request(req)
	return err
}

type igwDriftRecorderObject struct{}

func (igwDriftRecorderObject) ServiceName() string {
	return igwDriftRecorderObjectServiceName
}

func (igwDriftRecorderObject) Append(ctx restate.ObjectContext, req eventing.DriftReportRequest) error {
	reports, err := restate.Get[[]eventing.DriftReportRequest](ctx, "reports")
	if err != nil {
		return err
	}
	reports = append(reports, req)
	restate.Set(ctx, "reports", reports)
	return nil
}

func (igwDriftRecorderObject) List(ctx restate.ObjectSharedContext, _ restate.Void) ([]eventing.DriftReportRequest, error) {
	reports, err := restate.Get[[]eventing.DriftReportRequest](ctx, "reports")
	if err != nil || reports == nil {
		return nil, err
	}
	return reports, nil
}

type attachCall struct {
	InternetGatewayID string
	VpcID             string
}

type detachCall struct {
	InternetGatewayID string
	VpcID             string
}

type fakeIGWAPI struct {
	mu sync.Mutex

	nextID      string
	observed    map[string]ObservedState
	managedKeys map[string]string
	createCalls int
	attachCalls []attachCall
	detachCalls []detachCall
	updateCalls int
	deleteCalls int

	createFunc   func(context.Context, IGWSpec) (string, error)
	describeFunc func(context.Context, string) (ObservedState, error)
	deleteFunc   func(context.Context, string) error
	attachFunc   func(context.Context, string, string) error
	detachFunc   func(context.Context, string, string) error
	updateFunc   func(context.Context, string, map[string]string) error
	findFunc     func(context.Context, string) (string, error)
}

func newFakeIGWAPI() *fakeIGWAPI {
	return &fakeIGWAPI{
		nextID:      "igw-123",
		observed:    map[string]ObservedState{},
		managedKeys: map[string]string{},
	}
}

func (f *fakeIGWAPI) CreateInternetGateway(ctx context.Context, spec IGWSpec) (string, error) {
	if f.createFunc != nil {
		return f.createFunc(ctx, spec)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	id := f.nextID
	if id == "" {
		id = fmt.Sprintf("igw-%d", f.createCalls)
	}
	tags := map[string]string{"praxis:managed-key": spec.ManagedKey}
	maps.Copy(tags, spec.Tags)
	f.observed[id] = ObservedState{InternetGatewayId: id, Tags: tags}
	if spec.ManagedKey != "" {
		f.managedKeys[spec.ManagedKey] = id
	}
	return id, nil
}

func (f *fakeIGWAPI) DescribeInternetGateway(ctx context.Context, internetGatewayID string) (ObservedState, error) {
	if f.describeFunc != nil {
		return f.describeFunc(ctx, internetGatewayID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obs, ok := f.observed[internetGatewayID]
	if !ok {
		return ObservedState{}, &mockAPIError{code: "InvalidInternetGatewayID.NotFound", message: "missing"}
	}
	return cloneObserved(obs), nil
}

func (f *fakeIGWAPI) DeleteInternetGateway(ctx context.Context, internetGatewayID string) error {
	if f.deleteFunc != nil {
		return f.deleteFunc(ctx, internetGatewayID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	obs, ok := f.observed[internetGatewayID]
	if !ok {
		return &mockAPIError{code: "InvalidInternetGatewayID.NotFound", message: "missing"}
	}
	if obs.AttachedVpcId != "" {
		return &mockAPIError{code: "DependencyViolation", message: "still attached"}
	}
	delete(f.observed, internetGatewayID)
	for key, value := range f.managedKeys {
		if value == internetGatewayID {
			delete(f.managedKeys, key)
		}
	}
	return nil
}

func (f *fakeIGWAPI) AttachToVpc(ctx context.Context, internetGatewayID string, vpcID string) error {
	if f.attachFunc != nil {
		return f.attachFunc(ctx, internetGatewayID, vpcID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attachCalls = append(f.attachCalls, attachCall{InternetGatewayID: internetGatewayID, VpcID: vpcID})
	obs, ok := f.observed[internetGatewayID]
	if !ok {
		return &mockAPIError{code: "InvalidInternetGatewayID.NotFound", message: "missing"}
	}
	for id, other := range f.observed {
		if id != internetGatewayID && other.AttachedVpcId == vpcID {
			return &mockAPIError{code: "Resource.AlreadyAssociated", message: "already attached"}
		}
	}
	obs.AttachedVpcId = vpcID
	f.observed[internetGatewayID] = obs
	return nil
}

func (f *fakeIGWAPI) DetachFromVpc(ctx context.Context, internetGatewayID string, vpcID string) error {
	if f.detachFunc != nil {
		return f.detachFunc(ctx, internetGatewayID, vpcID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detachCalls = append(f.detachCalls, detachCall{InternetGatewayID: internetGatewayID, VpcID: vpcID})
	obs, ok := f.observed[internetGatewayID]
	if !ok {
		return &mockAPIError{code: "InvalidInternetGatewayID.NotFound", message: "missing"}
	}
	if obs.AttachedVpcId == "" || obs.AttachedVpcId != vpcID {
		return &mockAPIError{code: "Gateway.NotAttached", message: "not attached"}
	}
	obs.AttachedVpcId = ""
	f.observed[internetGatewayID] = obs
	return nil
}

func (f *fakeIGWAPI) UpdateTags(ctx context.Context, internetGatewayID string, tags map[string]string) error {
	if f.updateFunc != nil {
		return f.updateFunc(ctx, internetGatewayID, tags)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	obs, ok := f.observed[internetGatewayID]
	if !ok {
		return &mockAPIError{code: "InvalidInternetGatewayID.NotFound", message: "missing"}
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
	f.observed[internetGatewayID] = obs
	return nil
}

func (f *fakeIGWAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	if f.findFunc != nil {
		return f.findFunc(ctx, managedKey)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.managedKeys[managedKey], nil
}

func cloneObserved(obs ObservedState) ObservedState {
	clone := obs
	if obs.Tags != nil {
		clone.Tags = make(map[string]string, len(obs.Tags))
		maps.Copy(clone.Tags, obs.Tags)
	}
	return clone
}

func setupIGWDriver(t *testing.T, api IGWAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")

	driver := NewIGWDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(cfg aws.Config) IGWAPI {
		return api
	})
	env := restatetest.Start(t,
		restate.Reflect(driver),
		restate.Reflect(igwDriftRecorder{}),
		restate.Reflect(igwDriftRecorderObject{}),
	)
	return env.Ingress()
}

func pollIGWEventTypes(t *testing.T, client *ingress.Client, streamKey string, expected ...string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		records, err := ingress.Object[restate.Void, []eventing.DriftReportRequest](client, igwDriftRecorderObjectServiceName, streamKey, "List").Request(t.Context(), restate.Void{})
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

func testSpec(key, vpcID string, tags map[string]string) IGWSpec {
	if tags == nil {
		tags = map[string]string{"Name": "web-igw"}
	}
	return IGWSpec{
		Account:    "test",
		Region:     "us-east-1",
		VpcId:      vpcID,
		ManagedKey: key,
		Tags:       tags,
	}
}

func TestServiceName(t *testing.T) {
	drv := NewIGWDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestProvision_CreatesAndAttaches(t *testing.T) {
	api := newFakeIGWAPI()
	client := setupIGWDriver(t, api)
	key := "us-east-1~web-igw"

	outputs, err := ingress.Object[IGWSpec, IGWOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSpec(key, "vpc-123", map[string]string{"Name": "web-igw", "env": "dev"}))
	require.NoError(t, err)
	assert.Equal(t, "igw-123", outputs.InternetGatewayId)
	assert.Equal(t, "vpc-123", outputs.VpcId)
	assert.Equal(t, "available", outputs.State)
	assert.Equal(t, 1, api.createCalls)
	assert.Len(t, api.attachCalls, 1)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}

func TestProvision_MissingVpcIDFails(t *testing.T) {
	api := newFakeIGWAPI()
	client := setupIGWDriver(t, api)
	key := "us-east-1~web-igw"

	_, err := ingress.Object[IGWSpec, IGWOutputs](client, ServiceName, key, "Provision").Request(t.Context(), IGWSpec{Account: "test", Region: "us-east-1", ManagedKey: key})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vpcId is required")
}

func TestProvision_ConflictFails(t *testing.T) {
	api := newFakeIGWAPI()
	api.managedKeys["us-east-1~web-igw"] = "igw-existing"
	client := setupIGWDriver(t, api)

	_, err := ingress.Object[IGWSpec, IGWOutputs](client, ServiceName, "us-east-1~web-igw", "Provision").Request(t.Context(), testSpec("us-east-1~web-igw", "vpc-123", nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already managed by Praxis")
	assert.Equal(t, 0, api.createCalls)
}

func TestProvision_IdempotentReprovision(t *testing.T) {
	api := newFakeIGWAPI()
	client := setupIGWDriver(t, api)
	key := "us-east-1~web-igw"
	spec := testSpec(key, "vpc-123", map[string]string{"Name": "web-igw"})

	out1, err := ingress.Object[IGWSpec, IGWOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	out2, err := ingress.Object[IGWSpec, IGWOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.InternetGatewayId, out2.InternetGatewayId)
	assert.Equal(t, 1, api.createCalls)
	assert.Len(t, api.attachCalls, 1)
	assert.Equal(t, 0, api.updateCalls)
}

func TestProvision_VpcAttachmentChange(t *testing.T) {
	api := newFakeIGWAPI()
	client := setupIGWDriver(t, api)
	key := "us-east-1~web-igw"

	_, err := ingress.Object[IGWSpec, IGWOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSpec(key, "vpc-123", nil))
	require.NoError(t, err)

	outputs, err := ingress.Object[IGWSpec, IGWOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSpec(key, "vpc-456", nil))
	require.NoError(t, err)
	assert.Equal(t, "vpc-456", outputs.VpcId)
	assert.Len(t, api.detachCalls, 1)
	assert.Len(t, api.attachCalls, 2)
	assert.Equal(t, "vpc-123", api.detachCalls[0].VpcID)
	assert.Equal(t, "vpc-456", api.attachCalls[1].VpcID)
}

func TestProvision_TagUpdate(t *testing.T) {
	api := newFakeIGWAPI()
	client := setupIGWDriver(t, api)
	key := "us-east-1~web-igw"

	_, err := ingress.Object[IGWSpec, IGWOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSpec(key, "vpc-123", map[string]string{"Name": "web-igw", "env": "dev"}))
	require.NoError(t, err)

	_, err = ingress.Object[IGWSpec, IGWOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSpec(key, "vpc-123", map[string]string{"Name": "web-igw", "env": "prod"}))
	require.NoError(t, err)
	assert.Equal(t, 1, api.updateCalls)
	assert.Equal(t, "prod", api.observed["igw-123"].Tags["env"])
}

func TestProvision_VpcAlreadyHasIGWFails(t *testing.T) {
	api := newFakeIGWAPI()
	api.observed["igw-other"] = ObservedState{InternetGatewayId: "igw-other", AttachedVpcId: "vpc-123"}
	client := setupIGWDriver(t, api)
	key := "us-east-1~web-igw"

	_, err := ingress.Object[IGWSpec, IGWOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSpec(key, "vpc-123", nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already has an internet gateway attached")
}

func TestImport_ExistingIGW(t *testing.T) {
	api := newFakeIGWAPI()
	api.observed["igw-123"] = ObservedState{
		InternetGatewayId: "igw-123",
		AttachedVpcId:     "vpc-123",
		OwnerId:           "123456789012",
		Tags:              map[string]string{"Name": "web-igw", "praxis:managed-key": "us-east-1~web-igw"},
	}
	client := setupIGWDriver(t, api)
	key := "us-east-1~igw-123"

	outputs, err := ingress.Object[types.ImportRef, IGWOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "igw-123", Account: "test"})
	require.NoError(t, err)
	assert.Equal(t, "igw-123", outputs.InternetGatewayId)
	assert.Equal(t, "vpc-123", outputs.VpcId)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestDelete_DetachesAndDeletes(t *testing.T) {
	api := newFakeIGWAPI()
	client := setupIGWDriver(t, api)
	key := "us-east-1~web-igw"

	_, err := ingress.Object[IGWSpec, IGWOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSpec(key, "vpc-123", nil))
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Len(t, api.detachCalls, 1)
	assert.Equal(t, 1, api.deleteCalls)
	_, ok := api.observed["igw-123"]
	assert.False(t, ok)
}

func TestDelete_ObservedModeBlocked(t *testing.T) {
	api := newFakeIGWAPI()
	api.observed["igw-123"] = ObservedState{InternetGatewayId: "igw-123", AttachedVpcId: "vpc-123"}
	client := setupIGWDriver(t, api)
	key := "us-east-1~igw-123"

	_, err := ingress.Object[types.ImportRef, IGWOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "igw-123", Account: "test"})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Observed mode")
}

func TestReconcile_DetachedIGWReattaches(t *testing.T) {
	api := newFakeIGWAPI()
	client := setupIGWDriver(t, api)
	key := "us-east-1~web-igw"

	_, err := ingress.Object[IGWSpec, IGWOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSpec(key, "vpc-123", nil))
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.observed["igw-123"]
	obs.AttachedVpcId = ""
	api.observed["igw-123"] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Equal(t, "vpc-123", api.observed["igw-123"].AttachedVpcId)
	assert.ElementsMatch(t, []string{eventing.DriftEventDetected, eventing.DriftEventCorrected}, pollIGWEventTypes(t, client, key, eventing.DriftEventDetected, eventing.DriftEventCorrected))
}

func TestReconcile_ObservedModeReportsOnly(t *testing.T) {
	api := newFakeIGWAPI()
	api.observed["igw-123"] = ObservedState{
		InternetGatewayId: "igw-123",
		AttachedVpcId:     "vpc-123",
		Tags:              map[string]string{"Name": "web-igw"},
	}
	client := setupIGWDriver(t, api)
	key := "us-east-1~igw-123"

	_, err := ingress.Object[types.ImportRef, IGWOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "igw-123", Account: "test"})
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.observed["igw-123"]
	obs.AttachedVpcId = ""
	api.observed["igw-123"] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.False(t, result.Correcting)
	assert.Len(t, api.attachCalls, 0)
	assert.Contains(t, pollIGWEventTypes(t, client, key, eventing.DriftEventDetected), eventing.DriftEventDetected)
}

func TestReconcile_EmitsExternalDeleteEvent(t *testing.T) {
	api := newFakeIGWAPI()
	client := setupIGWDriver(t, api)
	key := "us-east-1~web-igw"

	_, err := ingress.Object[IGWSpec, IGWOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSpec(key, "vpc-123", nil))
	require.NoError(t, err)

	api.mu.Lock()
	delete(api.observed, "igw-123")
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "deleted externally")
	assert.Contains(t, pollIGWEventTypes(t, client, key, eventing.DriftEventExternalDelete), eventing.DriftEventExternalDelete)
}

func TestGetOutputs_ReturnsCurrentState(t *testing.T) {
	api := newFakeIGWAPI()
	client := setupIGWDriver(t, api)
	key := "us-east-1~web-igw"

	_, err := ingress.Object[IGWSpec, IGWOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSpec(key, "vpc-123", nil))
	require.NoError(t, err)

	outputs, err := ingress.Object[restate.Void, IGWOutputs](client, ServiceName, key, "GetOutputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, "igw-123", outputs.InternetGatewayId)
	assert.Equal(t, "vpc-123", outputs.VpcId)
}
