package lambda

import (
	"context"
	"maps"
	"sync"
	"testing"
	"time"

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

type statefulLambdaAPI struct {
	mu                               sync.Mutex
	item                             *ObservedState
	creates, reads, updates, deletes int
	createState                      string
}

func (f *statefulLambdaAPI) CreateFunction(_ context.Context, spec LambdaFunctionSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.item != nil {
		return "", &smithy.GenericAPIError{Code: "ResourceConflictException"}
	}
	f.creates++
	observed := lambdaObserved(spec)
	if f.createState != "" {
		observed.State = f.createState
	}
	f.item = &observed
	return observed.FunctionArn, nil
}
func (f *statefulLambdaAPI) UpdateFunctionCode(_ context.Context, spec LambdaFunctionSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.item == nil {
		return lambdaNotFound()
	}
	f.updates++
	f.item.ImageURI = spec.Code.ImageURI
	f.item.CodeSha256 = spec.Code.ZipFile + spec.Code.ImageURI
	return nil
}
func (f *statefulLambdaAPI) UpdateFunctionConfiguration(_ context.Context, spec LambdaFunctionSpec, _ ObservedState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.item == nil {
		return lambdaNotFound()
	}
	f.updates++
	tags := f.item.Tags
	observed := lambdaObserved(spec)
	observed.Tags = tags
	f.item = &observed
	return nil
}
func (f *statefulLambdaAPI) DescribeFunction(_ context.Context, name string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.item == nil || f.item.FunctionName != name {
		return ObservedState{}, lambdaNotFound()
	}
	observed := *f.item
	observed.Tags = maps.Clone(observed.Tags)
	return observed, nil
}
func (f *statefulLambdaAPI) DeleteFunction(_ context.Context, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.item == nil {
		return lambdaNotFound()
	}
	f.deletes++
	f.item = nil
	return nil
}
func (f *statefulLambdaAPI) UpdateTags(_ context.Context, _ string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.item == nil {
		return lambdaNotFound()
	}
	f.updates++
	f.item.Tags = maps.Clone(tags)
	return nil
}
func (f *statefulLambdaAPI) WaitForFunctionStable(context.Context, string, time.Duration) error {
	return nil
}
func (f *statefulLambdaAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}
func (f *statefulLambdaAPI) seed(spec LambdaFunctionSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed := lambdaObserved(spec)
	f.item = &observed
}
func (f *statefulLambdaAPI) remove() { f.mu.Lock(); defer f.mu.Unlock(); f.item = nil }
func (f *statefulLambdaAPI) forceState(state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.item.State = state
}
func lambdaObserved(spec LambdaFunctionSpec) ObservedState {
	return ObservedState{FunctionArn: "arn:lambda:" + spec.FunctionName, FunctionName: spec.FunctionName, Role: spec.Role, PackageType: spec.PackageType, Runtime: spec.Runtime, Handler: spec.Handler, Description: spec.Description, MemorySize: spec.MemorySize, Timeout: spec.Timeout, Environment: maps.Clone(spec.Environment), Layers: append([]string(nil), spec.Layers...), Architectures: append([]string(nil), spec.Architectures...), Tags: withManagedKey(spec.ManagedKey, spec.Tags), ImageURI: spec.Code.ImageURI, State: "Active", LastUpdateStatus: "Successful", CodeSha256: spec.Code.ZipFile + spec.Code.ImageURI}
}
func lambdaNotFound() error {
	return &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "not found"}
}

type lambdaSink struct{}

func (lambdaSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (lambdaSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }
func setupGenericLambda(t *testing.T, api LambdaAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericLambdaFunctionDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) LambdaAPI { return api })
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(lambdaSink{})).Ingress()
}
func genericLambdaSpec() LambdaFunctionSpec {
	return LambdaFunctionSpec{Account: "test", Region: "us-east-1", FunctionName: "fn", Role: "arn:role", Runtime: "go1.x", Handler: "bootstrap", Code: CodeSpec{ZipFile: "YQ=="}, Tags: map[string]string{"env": "test"}}
}
func TestGenericLambdaCoreAndImport(t *testing.T) {
	api := &statefulLambdaAPI{}
	client := setupGenericLambda(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[LambdaFunctionSpec, LambdaFunctionOutputs]{Client: client, ServiceName: ServiceName, Key: "us-east-1~fn", Spec: genericLambdaSpec(), Snapshot: api.snapshot, AssertInputs: func(t *testing.T, s LambdaFunctionSpec) { assert.Equal(t, "us-east-1~fn", s.ManagedKey) }})
	api = &statefulLambdaAPI{}
	spec := applyDefaults(genericLambdaSpec())
	spec.ManagedKey = "other"
	api.seed(spec)
	client = setupGenericLambda(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[LambdaFunctionOutputs]{Client: client, ServiceName: ServiceName, Key: "us-east-1~import", Ref: types.ImportRef{ResourceID: "fn", Account: "test"}, Snapshot: api.snapshot})
}
func TestGenericLambdaOpaqueChangeImmutableAndExternalDelete(t *testing.T) {
	api := &statefulLambdaAPI{}
	client := setupGenericLambda(t, api)
	key := "us-east-1~change"
	spec := genericLambdaSpec()
	_, err := ingress.Object[LambdaFunctionSpec, LambdaFunctionOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	before := api.snapshot().Updates
	spec.Code.ZipFile = "Yg=="
	_, err = ingress.Object[LambdaFunctionSpec, LambdaFunctionOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Greater(t, api.snapshot().Updates, before)
	spec.PackageType = "Image"
	spec.Code = CodeSpec{ImageURI: "repo:v1"}
	_, err = ingress.Object[LambdaFunctionSpec, LambdaFunctionOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "immutable")
	api = &statefulLambdaAPI{}
	client = setupGenericLambda(t, api)
	_, err = ingress.Object[LambdaFunctionSpec, LambdaFunctionOutputs](client, ServiceName, "us-east-1~gone", "Provision").Request(t.Context(), genericLambdaSpec())
	require.NoError(t, err)
	api.remove()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, "us-east-1~gone", "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericLambdaReadinessPendingAndFailed(t *testing.T) {
	api := &statefulLambdaAPI{createState: "Pending"}
	c := setupGenericLambda(t, api)
	key := "us-east-1~pending"
	_, err := ingress.Object[LambdaFunctionSpec, LambdaFunctionOutputs](c, ServiceName, key, "Provision").Request(t.Context(), genericLambdaSpec())
	require.NoError(t, err)
	status, err := ingress.Object[restate.Void, types.StatusResponse](c, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusPending, status.Status)
	api.forceState("Active")
	_, err = ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	status, err = ingress.Object[restate.Void, types.StatusResponse](c, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)

	api = &statefulLambdaAPI{createState: "Failed"}
	c = setupGenericLambda(t, api)
	_, err = ingress.Object[LambdaFunctionSpec, LambdaFunctionOutputs](c, ServiceName, "us-east-1~failed", "Provision").Request(t.Context(), genericLambdaSpec())
	require.Error(t, err)
}
