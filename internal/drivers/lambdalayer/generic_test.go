package lambdalayer

import (
	"context"
	"encoding/json"
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
	"sync"
	"testing"
)

type statefulLayerAPI struct {
	mu                               sync.Mutex
	versions                         map[int64]ObservedState
	creates, reads, updates, deletes int
	publishErr                       error
}

func newStatefulLayerAPI() *statefulLayerAPI {
	return &statefulLayerAPI{versions: map[int64]ObservedState{}}
}
func (f *statefulLayerAPI) PublishLayerVersion(_ context.Context, s LambdaLayerSpec) (LambdaLayerOutputs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.publishErr != nil {
		return LambdaLayerOutputs{}, f.publishErr
	}
	f.creates++
	v := int64(len(f.versions) + 1)
	o := layerObserved(v, s)
	f.versions[v] = o
	return outputsFromObserved(o), nil
}
func (f *statefulLayerAPI) GetLatestLayerVersion(_ context.Context, name string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	var latest int64
	for v := range f.versions {
		o := f.versions[v]
		if o.LayerName == name && v > latest {
			latest = v
		}
	}
	if latest == 0 {
		return ObservedState{}, layerNotFound()
	}
	return f.versions[latest], nil
}
func (f *statefulLayerAPI) DeleteLayerVersion(_ context.Context, _ string, v int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.versions[v]; !ok {
		return layerNotFound()
	}
	f.deletes++
	delete(f.versions, v)
	return nil
}
func (f *statefulLayerAPI) ListLayerVersions(_ context.Context, name string) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int64
	for v := range f.versions {
		o := f.versions[v]
		if o.LayerName == name {
			out = append(out, v)
		}
	}
	return out, nil
}
func (f *statefulLayerAPI) SyncLayerVersionPermissions(_ context.Context, _ string, v int64, p PermissionsSpec) (PermissionsSpec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.versions[v]
	if !ok {
		return PermissionsSpec{}, layerNotFound()
	}
	f.updates++
	o.Permissions = normalizePermissions(p)
	f.versions[v] = o
	return o.Permissions, nil
}
func (f *statefulLayerAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}
func (f *statefulLayerAPI) seed(s LambdaLayerSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions[1] = layerObserved(1, s)
}
func (f *statefulLayerAPI) externalPublish(s LambdaLayerSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := int64(len(f.versions) + 1)
	f.versions[v] = layerObserved(v, s)
}
func (f *statefulLayerAPI) failPublish(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishErr = err
}
func layerObserved(v int64, s LambdaLayerSpec) ObservedState {
	return ObservedState{LayerArn: "arn:layer:" + s.LayerName, LayerVersionArn: "arn:layer:" + s.LayerName + ":" + string(rune('0'+v)), LayerName: s.LayerName, Version: v, Description: s.Description, CompatibleRuntimes: append([]string(nil), s.CompatibleRuntimes...), CompatibleArchitectures: append([]string(nil), s.CompatibleArchitectures...), LicenseInfo: s.LicenseInfo, CodeSize: 1, CodeSha256: s.Code.ZipFile, Permissions: desiredPermissions(s)}
}
func layerNotFound() error {
	return &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "not found"}
}

type layerSink struct{}

func (layerSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (layerSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }
func setupGenericLayer(t *testing.T, api LayerAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	d := newGenericLambdaLayerDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) LayerAPI { return api })
	return restatetest.Start(t, restate.Reflect(d), restate.Reflect(layerSink{})).Ingress()
}
func genericLayerSpec() LambdaLayerSpec {
	return LambdaLayerSpec{Account: "test", Region: "us-east-1", LayerName: "common", Description: "v1", Code: CodeSpec{ZipFile: "YQ=="}, Permissions: &PermissionsSpec{AccountIds: []string{}}}
}

func provisionLayer(t *testing.T, client *ingress.Client, key string, spec LambdaLayerSpec, mode types.ReconcileMode) (LambdaLayerOutputs, error) {
	t.Helper()
	encoded, err := json.Marshal(spec)
	require.NoError(t, err)
	request := types.ProvisionRequest{Spec: encoded, Lifecycle: types.LifecyclePolicy{Reconcile: mode}}
	return ingress.Object[types.ProvisionRequest, LambdaLayerOutputs](client, ServiceName, key, "Provision").Request(t.Context(), request)
}
func TestGenericLayerCoreAndImport(t *testing.T) {
	api := newStatefulLayerAPI()
	c := setupGenericLayer(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[LambdaLayerSpec, LambdaLayerOutputs]{Client: c, ServiceName: ServiceName, Key: "us-east-1~layer", Spec: genericLayerSpec(), Snapshot: api.snapshot, AssertInputs: func(t *testing.T, s LambdaLayerSpec) { assert.Equal(t, "us-east-1~layer", s.ManagedKey) }})
	api = newStatefulLayerAPI()
	api.seed(genericLayerSpec())
	c = setupGenericLayer(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[LambdaLayerOutputs]{Client: c, ServiceName: ServiceName, Key: "us-east-1~import", Ref: types.ImportRef{ResourceID: "common", Account: "test"}, Snapshot: api.snapshot})
}
func TestGenericLayerPublishHookAndExternalVersionDrift(t *testing.T) {
	api := newStatefulLayerAPI()
	c := setupGenericLayer(t, api)
	key := "us-east-1~publish"
	s := genericLayerSpec()
	o, err := provisionLayer(t, c, key, s, types.ReconcileModeAuto)
	require.NoError(t, err)
	assert.Equal(t, int64(1), o.Version)
	s.Description = "v2"
	s.Code.ZipFile = "Yg=="
	o, err = provisionLayer(t, c, key, s, types.ReconcileModeAuto)
	require.NoError(t, err)
	assert.Equal(t, int64(2), o.Version)
	r, err := ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.False(t, r.Drift)
	external := s
	external.Description = "external"
	api.externalPublish(external)
	r, err = ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, r.Drift)
	assert.True(t, r.Correcting)
	assert.Empty(t, r.Error)
	assert.Equal(t, 3, api.snapshot().Creates, "external latest version is superseded by a desired republish")
	outputs, err := ingress.Object[restate.Void, LambdaLayerOutputs](c, ServiceName, key, "GetOutputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, int64(4), outputs.Version)
	before := api.snapshot()
	r, err = ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.False(t, r.Drift)
	assert.Equal(t, before.Creates, api.snapshot().Creates, "a corrected layer must reconcile idempotently")
	status, err := ingress.Object[restate.Void, types.StatusResponse](c, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Empty(t, status.Error)
}

func TestGenericLayerExternalVersionCorrectionFailureIsVisibleAndDoesNotAdvanceOutputs(t *testing.T) {
	api := newStatefulLayerAPI()
	c := setupGenericLayer(t, api)
	key := "us-east-1~publish-failure"
	spec := genericLayerSpec()
	outputs, err := provisionLayer(t, c, key, spec, types.ReconcileModeAuto)
	require.NoError(t, err)
	external := spec
	external.Description = "external"
	api.externalPublish(external)
	api.failPublish(&smithy.GenericAPIError{Code: "InvalidParameterValueException", Message: "invalid layer artifact"})

	result, err := ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Contains(t, result.Error, "invalid layer artifact")
	stored, err := ingress.Object[restate.Void, LambdaLayerOutputs](c, ServiceName, key, "GetOutputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, outputs.Version, stored.Version, "failed republish must not claim a new managed version")
	assert.Equal(t, types.StatusError, statusForLayer(t, c, key).Status)
}

func statusForLayer(t *testing.T, client *ingress.Client, key string) types.StatusResponse {
	t.Helper()
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return status
}
