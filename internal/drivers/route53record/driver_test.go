package route53record

import (
	"context"
	"fmt"
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

const route53RecordDriftRecorderObjectServiceName = "Route53RecordTestDriftRecorder"

type route53RecordDriftRecorder struct{}

func (route53RecordDriftRecorder) ServiceName() string {
	return eventing.ResourceEventBridgeServiceName
}

func (route53RecordDriftRecorder) ReportDrift(ctx restate.Context, req eventing.DriftReportRequest) error {
	_, err := restate.WithRequestType[eventing.DriftReportRequest, restate.Void](
		restate.Object[restate.Void](ctx, route53RecordDriftRecorderObjectServiceName, req.ResourceKey, "Append"),
	).Request(req)
	return err
}

type route53RecordDriftRecorderObject struct{}

func (route53RecordDriftRecorderObject) ServiceName() string {
	return route53RecordDriftRecorderObjectServiceName
}

func (route53RecordDriftRecorderObject) Append(ctx restate.ObjectContext, req eventing.DriftReportRequest) error {
	reports, err := restate.Get[[]eventing.DriftReportRequest](ctx, "reports")
	if err != nil {
		return err
	}
	reports = append(reports, req)
	restate.Set(ctx, "reports", reports)
	return nil
}

func (route53RecordDriftRecorderObject) List(ctx restate.ObjectSharedContext, _ restate.Void) ([]eventing.DriftReportRequest, error) {
	reports, err := restate.Get[[]eventing.DriftReportRequest](ctx, "reports")
	if err != nil || reports == nil {
		return nil, err
	}
	return reports, nil
}

type fakeRecordAPI struct {
	mu sync.Mutex

	records     map[string]ObservedState
	upsertCalls int
	deleteCalls int

	upsertFunc   func(context.Context, RecordSpec) error
	describeFunc func(context.Context, RecordIdentity) (ObservedState, error)
	deleteFunc   func(context.Context, ObservedState) error
}

func newFakeRecordAPI() *fakeRecordAPI {
	return &fakeRecordAPI{
		records: map[string]ObservedState{},
	}
}

func recordKey(identity RecordIdentity) string {
	key := fmt.Sprintf("%s|%s|%s", identity.HostedZoneId, normalizeRecordName(identity.Name), identity.Type)
	if identity.SetIdentifier != "" {
		key += "|" + identity.SetIdentifier
	}
	return key
}

func (f *fakeRecordAPI) UpsertRecord(ctx context.Context, spec RecordSpec) error {
	if f.upsertFunc != nil {
		return f.upsertFunc(ctx, spec)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertCalls++
	identity := identityFromSpec(spec)
	f.records[recordKey(identity)] = ObservedState{
		HostedZoneId:     spec.HostedZoneId,
		Name:             normalizeRecordName(spec.Name),
		Type:             spec.Type,
		TTL:              spec.TTL,
		ResourceRecords:  append([]string(nil), spec.ResourceRecords...),
		AliasTarget:      spec.AliasTarget,
		SetIdentifier:    spec.SetIdentifier,
		Weight:           spec.Weight,
		Region:           spec.Region,
		Failover:         spec.Failover,
		GeoLocation:      spec.GeoLocation,
		MultiValueAnswer: spec.MultiValueAnswer,
		HealthCheckId:    spec.HealthCheckId,
	}
	return nil
}

func (f *fakeRecordAPI) DescribeRecord(ctx context.Context, identity RecordIdentity) (ObservedState, error) {
	if f.describeFunc != nil {
		return f.describeFunc(ctx, identity)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := recordKey(identity)
	obs, ok := f.records[key]
	if !ok {
		return ObservedState{}, &mockAPIError{code: "InvalidInput", message: fmt.Sprintf("record %s %s not found in hosted zone %s", identity.Name, identity.Type, identity.HostedZoneId)}
	}
	return obs, nil
}

func (f *fakeRecordAPI) DeleteRecord(ctx context.Context, observed ObservedState) error {
	if f.deleteFunc != nil {
		return f.deleteFunc(ctx, observed)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	identity := RecordIdentity{HostedZoneId: observed.HostedZoneId, Name: observed.Name, Type: observed.Type, SetIdentifier: observed.SetIdentifier}
	delete(f.records, recordKey(identity))
	return nil
}

func setupRecordDriver(t *testing.T, api RecordAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")

	driver := NewDNSRecordDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(cfg aws.Config) RecordAPI {
		return api
	})
	env := restatetest.Start(t,
		restate.Reflect(driver),
		restate.Reflect(route53RecordDriftRecorder{}),
		restate.Reflect(route53RecordDriftRecorderObject{}),
	)
	return env.Ingress()
}

func pollRoute53RecordEventTypes(t *testing.T, client *ingress.Client, resourceKey string, expected ...string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		records, err := ingress.Object[restate.Void, []eventing.DriftReportRequest](client, route53RecordDriftRecorderObjectServiceName, resourceKey, "List").Request(t.Context(), restate.Void{})
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

func testRecordSpec(hostedZoneId, name, recordType string) RecordSpec {
	return RecordSpec{
		Account:         "test",
		HostedZoneId:    hostedZoneId,
		Name:            name,
		Type:            recordType,
		TTL:             300,
		ResourceRecords: []string{"1.2.3.4"},
	}
}

func TestRecordServiceName(t *testing.T) {
	drv := NewDNSRecordDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestRecordProvision_CreatesRecord(t *testing.T) {
	api := newFakeRecordAPI()
	client := setupRecordDriver(t, api)
	key := "Z123~example.com~A"

	outputs, err := ingress.Object[RecordSpec, RecordOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testRecordSpec("Z123", "example.com", "A"))
	require.NoError(t, err)
	assert.Equal(t, "Z123", outputs.HostedZoneId)
	assert.Equal(t, "example.com", outputs.FQDN)
	assert.Equal(t, "A", outputs.Type)
	assert.Equal(t, 1, api.upsertCalls)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}

func TestRecordProvision_Idempotent(t *testing.T) {
	api := newFakeRecordAPI()
	client := setupRecordDriver(t, api)
	key := "Z123~example.com~A"
	spec := testRecordSpec("Z123", "example.com", "A")

	out1, err := ingress.Object[RecordSpec, RecordOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	out2, err := ingress.Object[RecordSpec, RecordOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.FQDN, out2.FQDN)
}

func TestRecordProvision_MissingHostedZoneIdFails(t *testing.T) {
	api := newFakeRecordAPI()
	client := setupRecordDriver(t, api)
	key := "Z123~example.com~A"

	_, err := ingress.Object[RecordSpec, RecordOutputs](client, ServiceName, key, "Provision").Request(t.Context(), RecordSpec{Account: "test", Name: "example.com", Type: "A", TTL: 300, ResourceRecords: []string{"1.2.3.4"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hostedZoneId is required")
}

func TestRecordImport_ExistingRecord(t *testing.T) {
	api := newFakeRecordAPI()
	api.records["Z123|example.com|A"] = ObservedState{
		HostedZoneId:    "Z123",
		Name:            "example.com",
		Type:            "A",
		TTL:             300,
		ResourceRecords: []string{"1.2.3.4"},
	}
	client := setupRecordDriver(t, api)
	key := "Z123~example.com~A"

	outputs, err := ingress.Object[types.ImportRef, RecordOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "Z123/example.com/A", Account: "test"})
	require.NoError(t, err)
	assert.Equal(t, "Z123", outputs.HostedZoneId)
	assert.Equal(t, "example.com", outputs.FQDN)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestRecordDelete_DeletesRecord(t *testing.T) {
	api := newFakeRecordAPI()
	client := setupRecordDriver(t, api)
	key := "Z123~example.com~A"

	_, err := ingress.Object[RecordSpec, RecordOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testRecordSpec("Z123", "example.com", "A"))
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, 1, api.deleteCalls)
}

func TestRecordDelete_ObservedModeBlocked(t *testing.T) {
	api := newFakeRecordAPI()
	api.records["Z123|example.com|A"] = ObservedState{
		HostedZoneId:    "Z123",
		Name:            "example.com",
		Type:            "A",
		TTL:             300,
		ResourceRecords: []string{"1.2.3.4"},
	}
	client := setupRecordDriver(t, api)
	key := "Z123~example.com~A"

	_, err := ingress.Object[types.ImportRef, RecordOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "Z123/example.com/A", Account: "test"})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Observed mode")
}

func TestRecordReconcile_TTLDriftCorrected(t *testing.T) {
	api := newFakeRecordAPI()
	client := setupRecordDriver(t, api)
	key := "Z123~example.com~A"

	_, err := ingress.Object[RecordSpec, RecordOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testRecordSpec("Z123", "example.com", "A"))
	require.NoError(t, err)

	api.mu.Lock()
	rk := "Z123|example.com|A"
	obs := api.records[rk]
	obs.TTL = 60
	api.records[rk] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Equal(t, []string{eventing.DriftEventDetected, eventing.DriftEventCorrected}, pollRoute53RecordEventTypes(t, client, key, eventing.DriftEventDetected, eventing.DriftEventCorrected))
}

func TestRecordReconcile_ObservedModeReportsOnly_EmitsDetected(t *testing.T) {
	api := newFakeRecordAPI()
	api.records["Z123|example.com|A"] = ObservedState{
		HostedZoneId:    "Z123",
		Name:            "example.com",
		Type:            "A",
		TTL:             300,
		ResourceRecords: []string{"1.2.3.4"},
	}
	client := setupRecordDriver(t, api)
	key := "Z123~example.com~A"

	_, err := ingress.Object[types.ImportRef, RecordOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "Z123/example.com/A", Account: "test"})
	require.NoError(t, err)

	api.mu.Lock()
	rk := "Z123|example.com|A"
	obs := api.records[rk]
	obs.TTL = 60
	api.records[rk] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.False(t, result.Correcting)
	assert.Equal(t, []string{eventing.DriftEventDetected}, pollRoute53RecordEventTypes(t, client, key, eventing.DriftEventDetected))
}

func TestRecordReconcile_ExternalDelete_EmitsEvent(t *testing.T) {
	api := newFakeRecordAPI()
	client := setupRecordDriver(t, api)
	key := "Z123~example.com~A"

	_, err := ingress.Object[RecordSpec, RecordOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testRecordSpec("Z123", "example.com", "A"))
	require.NoError(t, err)

	api.mu.Lock()
	delete(api.records, "Z123|example.com|A")
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "deleted externally")
	assert.Equal(t, []string{eventing.DriftEventExternalDelete}, pollRoute53RecordEventTypes(t, client, key, eventing.DriftEventExternalDelete))
}

func TestRecordGetOutputs_ReturnsCurrentState(t *testing.T) {
	api := newFakeRecordAPI()
	client := setupRecordDriver(t, api)
	key := "Z123~example.com~A"

	_, err := ingress.Object[RecordSpec, RecordOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testRecordSpec("Z123", "example.com", "A"))
	require.NoError(t, err)

	outputs, err := ingress.Object[restate.Void, RecordOutputs](client, ServiceName, key, "GetOutputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, "Z123", outputs.HostedZoneId)
	assert.Equal(t, "example.com", outputs.FQDN)
	assert.Equal(t, "A", outputs.Type)
}
