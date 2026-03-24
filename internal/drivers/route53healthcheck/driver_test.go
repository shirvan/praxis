package route53healthcheck

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"

	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"
	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/pkg/types"
)

type fakeHealthCheckAPI struct {
	mu sync.Mutex

	nextID      string
	checks      map[string]ObservedState
	createCalls int
	updateCalls int
	deleteCalls int
	tagCalls    int

	createFunc   func(context.Context, HealthCheckSpec) (string, error)
	describeFunc func(context.Context, string) (ObservedState, error)
	deleteFunc   func(context.Context, string) error
}

func newFakeHealthCheckAPI() *fakeHealthCheckAPI {
	return &fakeHealthCheckAPI{
		nextID: "hc-123",
		checks: map[string]ObservedState{},
	}
}

func (f *fakeHealthCheckAPI) CreateHealthCheck(ctx context.Context, spec HealthCheckSpec) (string, error) {
	if f.createFunc != nil {
		return f.createFunc(ctx, spec)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	id := f.nextID
	if id == "" {
		id = fmt.Sprintf("hc-%d", f.createCalls)
	}
	normalized, _ := normalizeHealthCheckSpec(spec)
	f.checks[id] = ObservedState{
		HealthCheckId:                id,
		Version:                      1,
		Type:                         normalized.Type,
		IPAddress:                    normalized.IPAddress,
		Port:                         normalized.Port,
		ResourcePath:                 normalized.ResourcePath,
		FQDN:                         normalized.FQDN,
		SearchString:                 normalized.SearchString,
		RequestInterval:              normalized.RequestInterval,
		FailureThreshold:             normalized.FailureThreshold,
		EnableSNI:                    normalized.EnableSNI,
		Disabled:                     normalized.Disabled,
		InvertHealthCheck:            normalized.InvertHealthCheck,
		ChildHealthChecks:            normalized.ChildHealthChecks,
		HealthThreshold:              normalized.HealthThreshold,
		CloudWatchAlarmName:          normalized.CloudWatchAlarmName,
		CloudWatchAlarmRegion:        normalized.CloudWatchAlarmRegion,
		InsufficientDataHealthStatus: normalized.InsufficientDataHealthStatus,
		Regions:                      normalized.Regions,
		Tags:                         copyTags(normalized.Tags),
	}
	return id, nil
}

func (f *fakeHealthCheckAPI) DescribeHealthCheck(ctx context.Context, healthCheckID string) (ObservedState, error) {
	if f.describeFunc != nil {
		return f.describeFunc(ctx, healthCheckID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obs, ok := f.checks[healthCheckID]
	if !ok {
		return ObservedState{}, &mockAPIError{code: "NoSuchHealthCheck", message: "not found"}
	}
	return cloneObserved(obs), nil
}

func (f *fakeHealthCheckAPI) UpdateHealthCheck(ctx context.Context, healthCheckID string, observed ObservedState, desired HealthCheckSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	obs, ok := f.checks[healthCheckID]
	if !ok {
		return &mockAPIError{code: "NoSuchHealthCheck", message: "not found"}
	}
	normalized, _ := normalizeHealthCheckSpec(desired)
	obs.IPAddress = normalized.IPAddress
	obs.Port = normalized.Port
	obs.ResourcePath = normalized.ResourcePath
	obs.FQDN = normalized.FQDN
	obs.SearchString = normalized.SearchString
	obs.FailureThreshold = normalized.FailureThreshold
	obs.EnableSNI = normalized.EnableSNI
	obs.Disabled = normalized.Disabled
	obs.InvertHealthCheck = normalized.InvertHealthCheck
	obs.ChildHealthChecks = normalized.ChildHealthChecks
	obs.HealthThreshold = normalized.HealthThreshold
	obs.Regions = normalized.Regions
	obs.Version++
	f.checks[healthCheckID] = obs
	return nil
}

func (f *fakeHealthCheckAPI) UpdateTags(ctx context.Context, healthCheckID string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tagCalls++
	obs, ok := f.checks[healthCheckID]
	if !ok {
		return &mockAPIError{code: "NoSuchHealthCheck", message: "not found"}
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
	f.checks[healthCheckID] = obs
	return nil
}

func (f *fakeHealthCheckAPI) DeleteHealthCheck(ctx context.Context, healthCheckID string) error {
	if f.deleteFunc != nil {
		return f.deleteFunc(ctx, healthCheckID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	if _, ok := f.checks[healthCheckID]; !ok {
		return &mockAPIError{code: "NoSuchHealthCheck", message: "not found"}
	}
	delete(f.checks, healthCheckID)
	return nil
}

func cloneObserved(obs ObservedState) ObservedState {
	clone := obs
	if obs.Tags != nil {
		clone.Tags = make(map[string]string, len(obs.Tags))
		maps.Copy(clone.Tags, obs.Tags)
	}
	if obs.ChildHealthChecks != nil {
		clone.ChildHealthChecks = make([]string, len(obs.ChildHealthChecks))
		copy(clone.ChildHealthChecks, obs.ChildHealthChecks)
	}
	if obs.Regions != nil {
		clone.Regions = make([]string, len(obs.Regions))
		copy(clone.Regions, obs.Regions)
	}
	return clone
}

func copyTags(tags map[string]string) map[string]string {
	if tags == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(tags))
	maps.Copy(out, tags)
	return out
}

func setupHealthCheckDriver(t *testing.T, api HealthCheckAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")

	driver := NewHealthCheckDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(cfg aws.Config) HealthCheckAPI {
		return api
	})
	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress()
}

func testHTTPSpec(tags map[string]string) HealthCheckSpec {
	if tags == nil {
		tags = map[string]string{"env": "dev"}
	}
	return HealthCheckSpec{
		Account:   "test",
		Type:      "HTTP",
		IPAddress: "1.2.3.4",
		Port:      80,
		Tags:      tags,
	}
}

func TestHealthCheckServiceName(t *testing.T) {
	drv := NewHealthCheckDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestHealthCheckProvision_Creates(t *testing.T) {
	api := newFakeHealthCheckAPI()
	client := setupHealthCheckDriver(t, api)
	key := "web-http-check"

	outputs, err := ingress.Object[HealthCheckSpec, HealthCheckOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testHTTPSpec(map[string]string{"env": "dev"}))
	require.NoError(t, err)
	assert.Equal(t, "hc-123", outputs.HealthCheckId)
	assert.Equal(t, 1, api.createCalls)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}

func TestHealthCheckProvision_Idempotent(t *testing.T) {
	api := newFakeHealthCheckAPI()
	client := setupHealthCheckDriver(t, api)
	key := "web-http-check"
	spec := testHTTPSpec(map[string]string{"env": "dev"})

	out1, err := ingress.Object[HealthCheckSpec, HealthCheckOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	out2, err := ingress.Object[HealthCheckSpec, HealthCheckOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.HealthCheckId, out2.HealthCheckId)
	assert.Equal(t, 1, api.createCalls)
}

func TestHealthCheckProvision_MissingTypeFails(t *testing.T) {
	api := newFakeHealthCheckAPI()
	client := setupHealthCheckDriver(t, api)
	key := "bad-check"

	_, err := ingress.Object[HealthCheckSpec, HealthCheckOutputs](client, ServiceName, key, "Provision").Request(t.Context(), HealthCheckSpec{Account: "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "type is required")
}

func TestHealthCheckProvision_TagUpdate(t *testing.T) {
	api := newFakeHealthCheckAPI()
	client := setupHealthCheckDriver(t, api)
	key := "web-http-check"

	_, err := ingress.Object[HealthCheckSpec, HealthCheckOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testHTTPSpec(map[string]string{"env": "dev"}))
	require.NoError(t, err)

	_, err = ingress.Object[HealthCheckSpec, HealthCheckOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testHTTPSpec(map[string]string{"env": "prod"}))
	require.NoError(t, err)
	assert.Equal(t, "prod", api.checks["hc-123"].Tags["env"])
}

func TestHealthCheckImport_Existing(t *testing.T) {
	api := newFakeHealthCheckAPI()
	api.checks["hc-123"] = ObservedState{
		HealthCheckId:    "hc-123",
		Version:          1,
		Type:             "HTTP",
		IPAddress:        "1.2.3.4",
		Port:             80,
		FailureThreshold: 3,
		RequestInterval:  30,
		Tags:             map[string]string{"env": "prod"},
	}
	client := setupHealthCheckDriver(t, api)
	key := "web-http-check"

	outputs, err := ingress.Object[types.ImportRef, HealthCheckOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "hc-123", Account: "test"})
	require.NoError(t, err)
	assert.Equal(t, "hc-123", outputs.HealthCheckId)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestHealthCheckDelete_Deletes(t *testing.T) {
	api := newFakeHealthCheckAPI()
	client := setupHealthCheckDriver(t, api)
	key := "web-http-check"

	_, err := ingress.Object[HealthCheckSpec, HealthCheckOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testHTTPSpec(nil))
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, 1, api.deleteCalls)
	_, ok := api.checks["hc-123"]
	assert.False(t, ok)
}

func TestHealthCheckDelete_ObservedModeBlocked(t *testing.T) {
	api := newFakeHealthCheckAPI()
	api.checks["hc-123"] = ObservedState{
		HealthCheckId: "hc-123", Version: 1, Type: "HTTP", IPAddress: "1.2.3.4", Port: 80, FailureThreshold: 3, RequestInterval: 30, Tags: map[string]string{},
	}
	client := setupHealthCheckDriver(t, api)
	key := "web-http-check"

	_, err := ingress.Object[types.ImportRef, HealthCheckOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "hc-123", Account: "test"})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Observed mode")
}

func TestHealthCheckReconcile_PortDriftCorrected(t *testing.T) {
	api := newFakeHealthCheckAPI()
	client := setupHealthCheckDriver(t, api)
	key := "web-http-check"

	_, err := ingress.Object[HealthCheckSpec, HealthCheckOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testHTTPSpec(map[string]string{}))
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.checks["hc-123"]
	obs.Port = 8080
	api.checks["hc-123"] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
}

func TestHealthCheckReconcile_ObservedModeReportsOnly(t *testing.T) {
	api := newFakeHealthCheckAPI()
	api.checks["hc-123"] = ObservedState{
		HealthCheckId: "hc-123", Version: 1, Type: "HTTP", IPAddress: "1.2.3.4", Port: 80, FailureThreshold: 3, RequestInterval: 30, Tags: map[string]string{},
	}
	client := setupHealthCheckDriver(t, api)
	key := "web-http-check"

	_, err := ingress.Object[types.ImportRef, HealthCheckOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "hc-123", Account: "test"})
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.checks["hc-123"]
	obs.Port = 8080
	api.checks["hc-123"] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.False(t, result.Correcting)
}

func TestHealthCheckGetOutputs_ReturnsCurrentState(t *testing.T) {
	api := newFakeHealthCheckAPI()
	client := setupHealthCheckDriver(t, api)
	key := "web-http-check"

	_, err := ingress.Object[HealthCheckSpec, HealthCheckOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testHTTPSpec(nil))
	require.NoError(t, err)

	outputs, err := ingress.Object[restate.Void, HealthCheckOutputs](client, ServiceName, key, "GetOutputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, "hc-123", outputs.HealthCheckId)
}
