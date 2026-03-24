package route53record

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/pkg/types"
)

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
		return ObservedState{}, fmt.Errorf("record %s %s not found in hosted zone %s", identity.Name, identity.Type, identity.HostedZoneId)
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
	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress()
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
