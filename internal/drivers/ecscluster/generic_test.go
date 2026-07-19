package ecscluster

import (
	"context"
	"errors"
	"maps"
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

type statefulECSAPI struct {
	mu                               sync.Mutex
	clusters                         map[string]ObservedState
	creates, reads, updates, deletes int
	failCreateResponseOnce           bool
}

func newStatefulECSAPI() *statefulECSAPI {
	return &statefulECSAPI{clusters: map[string]ObservedState{}}
}

func (f *statefulECSAPI) CreateCluster(_ context.Context, spec ECSClusterSpec) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.clusters[spec.Name]; ok {
		return ObservedState{}, &smithy.GenericAPIError{Code: "ClusterAlreadyExistsException", Message: "exists"}
	}
	f.creates++
	obs := ObservedState{ARN: "arn:aws:ecs:us-east-1:123456789012:cluster/" + spec.Name, Name: spec.Name, Status: "ACTIVE", ContainerInsights: normalizeContainerInsights(spec.ContainerInsights), CapacityProviders: append([]string(nil), spec.CapacityProviders...), Tags: managedTags(spec.Tags, spec.ManagedKey)}
	f.clusters[spec.Name] = obs
	if f.failCreateResponseOnce {
		f.failCreateResponseOnce = false
		return ObservedState{}, errors.New("timeout after CreateCluster response was lost")
	}
	return cloneECS(obs), nil
}
func (f *statefulECSAPI) DescribeCluster(_ context.Context, name string) (ObservedState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	obs, ok := f.clusters[name]
	return cloneECS(obs), ok, nil
}
func (f *statefulECSAPI) UpdateCluster(_ context.Context, name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	obs, ok := f.clusters[name]
	if !ok {
		return &smithy.GenericAPIError{Code: "ClusterNotFoundException"}
	}
	f.updates++
	obs.ContainerInsights = value
	f.clusters[name] = obs
	return nil
}
func (f *statefulECSAPI) PutCapacityProviders(_ context.Context, name string, providers []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	obs, ok := f.clusters[name]
	if !ok {
		return &smithy.GenericAPIError{Code: "ClusterNotFoundException"}
	}
	f.updates++
	obs.CapacityProviders = append([]string(nil), providers...)
	f.clusters[name] = obs
	return nil
}
func (f *statefulECSAPI) DeleteCluster(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.clusters[name]; !ok {
		return &smithy.GenericAPIError{Code: "ClusterNotFoundException"}
	}
	f.deletes++
	delete(f.clusters, name)
	return nil
}
func (f *statefulECSAPI) TagResource(_ context.Context, arn string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, obs := range f.clusters {
		if obs.ARN == arn {
			f.updates++
			if obs.Tags == nil {
				obs.Tags = map[string]string{}
			}
			maps.Copy(obs.Tags, tags)
			f.clusters[name] = obs
			return nil
		}
	}
	return &smithy.GenericAPIError{Code: "ClusterNotFoundException"}
}
func (f *statefulECSAPI) UntagResource(_ context.Context, arn string, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, obs := range f.clusters {
		if obs.ARN == arn {
			f.updates++
			for _, k := range keys {
				delete(obs.Tags, k)
			}
			f.clusters[name] = obs
			return nil
		}
	}
	return &smithy.GenericAPIError{Code: "ClusterNotFoundException"}
}
func (f *statefulECSAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}
func (f *statefulECSAPI) seed(obs ObservedState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clusters[obs.Name] = cloneECS(obs)
}
func (f *statefulECSAPI) remove(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.clusters, name)
}
func (f *statefulECSAPI) mutate(name string, fn func(*ObservedState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obs := f.clusters[name]
	fn(&obs)
	f.clusters[name] = obs
}
func (f *statefulECSAPI) get(name string) ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneECS(f.clusters[name])
}
func cloneECS(obs ObservedState) ObservedState {
	obs.Tags = maps.Clone(obs.Tags)
	obs.CapacityProviders = append([]string(nil), obs.CapacityProviders...)
	return obs
}

type ecsDriftSink struct{}

func (ecsDriftSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (ecsDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

func setupGenericECS(t *testing.T, api ECSClusterAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	d := newGenericECSClusterDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) ECSClusterAPI { return api })
	return restatetest.Start(t, restate.Reflect(d), restate.Reflect(ecsDriftSink{})).Ingress()
}
func ecsSpec(name string) ECSClusterSpec {
	return ECSClusterSpec{Account: "test", Region: "us-east-1", Name: name, ContainerInsights: "enabled", CapacityProviders: []string{"FARGATE"}, Tags: map[string]string{"env": "test"}}
}

func TestGenericECSCoreLifecycle(t *testing.T) {
	api := newStatefulECSAPI()
	c := setupGenericECS(t, api)
	spec := ecsSpec("core")
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[ECSClusterSpec, ECSClusterOutputs]{Client: c, ServiceName: ServiceName, Key: "us-east-1~core", Spec: spec, Snapshot: api.snapshot, AssertInputs: func(t *testing.T, got ECSClusterSpec) {
		assert.Equal(t, "us-east-1~core", got.ManagedKey)
		assert.Equal(t, spec.Name, got.Name)
	}})
}
func TestGenericECSObservedImportLifecycle(t *testing.T) {
	api := newStatefulECSAPI()
	api.seed(ObservedState{ARN: "arn:cluster/imported", Name: "imported", Status: "ACTIVE", ContainerInsights: "disabled", Tags: map[string]string{"env": "prod"}})
	c := setupGenericECS(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[ECSClusterOutputs]{Client: c, ServiceName: ServiceName, Key: "us-east-1~imported", Ref: types.ImportRef{ResourceID: "imported", Account: "test"}, Snapshot: api.snapshot})
}
func TestGenericECSAmbiguousCreateRecoversExactOwnership(t *testing.T) {
	api := newStatefulECSAPI()
	api.failCreateResponseOnce = true
	c := setupGenericECS(t, api)
	out := provisionECS(t, c, "us-east-1~ambiguous", ecsSpec("ambiguous"))
	assert.Equal(t, "ambiguous", out.Name)
	assert.Equal(t, 1, api.snapshot().Creates)
}
func TestGenericECSRejectsUnownedNameCollision(t *testing.T) {
	api := newStatefulECSAPI()
	api.seed(ObservedState{ARN: "arn:collision", Name: "collision", Status: "ACTIVE", Tags: map[string]string{}})
	c := setupGenericECS(t, api)
	_, err := ingress.Object[types.ProvisionRequest, ECSClusterOutputs](c, ServiceName, "us-east-1~collision", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, ecsSpec("collision")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without exact Praxis ownership")
}
func TestGenericECSConvergesCompositeDrift(t *testing.T) {
	api := newStatefulECSAPI()
	c := setupGenericECS(t, api)
	spec := ecsSpec("drift")
	key := "us-east-1~drift"
	provisionECS(t, c, key, spec)
	api.mutate("drift", func(obs *ObservedState) {
		obs.ContainerInsights = "disabled"
		obs.CapacityProviders = nil
		obs.Tags["env"] = "stale"
	})
	result, err := ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	obs := api.get("drift")
	assert.Equal(t, "enabled", obs.ContainerInsights)
	assert.Equal(t, []string{"FARGATE"}, obs.CapacityProviders)
	assert.Equal(t, "test", obs.Tags["env"])
}
func TestGenericECSRejectsImmutableNameChange(t *testing.T) {
	api := newStatefulECSAPI()
	c := setupGenericECS(t, api)
	spec := ecsSpec("fixed")
	key := "us-east-1~fixed"
	provisionECS(t, c, key, spec)
	spec.Name = "other"
	_, err := ingress.Object[types.ProvisionRequest, ECSClusterOutputs](c, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is immutable")
}
func TestGenericECSExternalDeleteRequiresReplacement(t *testing.T) {
	api := newStatefulECSAPI()
	c := setupGenericECS(t, api)
	spec := ecsSpec("gone")
	key := "us-east-1~gone"
	provisionECS(t, c, key, spec)
	api.remove("gone")
	before := api.snapshot()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Equal(t, before.Creates, api.snapshot().Creates)
}
func TestECSValidation(t *testing.T) {
	assert.NoError(t, validateSpec(ecsSpec("ok")))
	bad := ecsSpec("")
	assert.Error(t, validateSpec(bad))
	bad = ecsSpec("x")
	bad.ContainerInsights = "bogus"
	assert.Error(t, validateSpec(bad))
}
func TestECSClassifiers(t *testing.T) {
	assert.True(t, IsInvalidParam(&smithy.GenericAPIError{Code: "InvalidParameterException"}))
	assert.False(t, IsInvalidParam(nil))
}
func provisionECS(t *testing.T, c *ingress.Client, key string, spec ECSClusterSpec) ECSClusterOutputs {
	t.Helper()
	out, err := ingress.Object[types.ProvisionRequest, ECSClusterOutputs](c, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	return out
}
