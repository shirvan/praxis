package targetgroup

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

type statefulTGAPI struct {
	mu                               sync.Mutex
	groups                           map[string]*ObservedState
	creates, reads, updates, deletes int
	failCreateOnce                   bool
}

func newStatefulTGAPI() *statefulTGAPI { return &statefulTGAPI{groups: map[string]*ObservedState{}} }
func (f *statefulTGAPI) CreateTargetGroup(_ context.Context, s TargetGroupSpec) (TargetGroupOutputs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.groups[s.Name]; ok {
		return TargetGroupOutputs{}, &smithy.GenericAPIError{Code: "DuplicateTargetGroupName"}
	}
	f.creates++
	obs := observedTG(s)
	f.groups[s.Name] = &obs
	if f.failCreateOnce {
		f.failCreateOnce = false
		return TargetGroupOutputs{}, errors.New("timeout after create response lost")
	}
	return outputsFromObserved(obs), nil
}
func (f *statefulTGAPI) DescribeTargetGroup(_ context.Context, id string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	for name := range f.groups {
		obs := f.groups[name]
		if obs.Name == id || obs.TargetGroupArn == id {
			return cloneTG(*obs), nil
		}
	}
	return ObservedState{}, &smithy.GenericAPIError{Code: "TargetGroupNotFound"}
}
func (f *statefulTGAPI) DeleteTargetGroup(_ context.Context, arn string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for n := range f.groups {
		o := f.groups[n]
		if o.TargetGroupArn == arn {
			f.deletes++
			delete(f.groups, n)
			return nil
		}
	}
	return &smithy.GenericAPIError{Code: "TargetGroupNotFound"}
}
func (f *statefulTGAPI) ModifyTargetGroup(_ context.Context, arn string, s TargetGroupSpec) error {
	return f.mutate(arn, func(o *ObservedState) { o.HealthCheck = s.HealthCheck })
}
func (f *statefulTGAPI) UpdateAttributes(_ context.Context, arn string, s TargetGroupSpec) error {
	return f.mutate(arn, func(o *ObservedState) { o.DeregistrationDelay = s.DeregistrationDelay; o.Stickiness = s.Stickiness })
}
func (f *statefulTGAPI) UpdateTargets(_ context.Context, arn string, d, _ []Target) error {
	return f.mutate(arn, func(o *ObservedState) { o.Targets = append([]Target(nil), d...) })
}
func (f *statefulTGAPI) UpdateTags(_ context.Context, arn string, d map[string]string) error {
	return f.mutate(arn, func(o *ObservedState) { o.Tags = maps.Clone(d) })
}
func (f *statefulTGAPI) mutate(arn string, fn func(*ObservedState)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for n := range f.groups {
		o := f.groups[n]
		if o.TargetGroupArn == arn {
			f.updates++
			updated := cloneTG(*o)
			fn(&updated)
			f.groups[n] = &updated
			return nil
		}
	}
	return &smithy.GenericAPIError{Code: "TargetGroupNotFound"}
}
func (f *statefulTGAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}
func (f *statefulTGAPI) seed(o ObservedState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cloned := cloneTG(o)
	f.groups[o.Name] = &cloned
}
func (f *statefulTGAPI) remove(n string) { f.mu.Lock(); defer f.mu.Unlock(); delete(f.groups, n) }
func (f *statefulTGAPI) force(n string, fn func(*ObservedState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var current ObservedState
	if stored := f.groups[n]; stored != nil {
		current = cloneTG(*stored)
	}
	fn(&current)
	f.groups[n] = &current
}
func (f *statefulTGAPI) get(n string) ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if stored := f.groups[n]; stored != nil {
		return cloneTG(*stored)
	}
	return ObservedState{}
}
func cloneTG(o ObservedState) ObservedState {
	o.Tags = maps.Clone(o.Tags)
	o.Targets = append([]Target(nil), o.Targets...)
	return o
}
func observedTG(s TargetGroupSpec) ObservedState {
	return ObservedState{TargetGroupArn: "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/" + s.Name + "/1", Name: s.Name, Protocol: s.Protocol, Port: s.Port, VpcId: s.VpcId, TargetType: s.TargetType, ProtocolVersion: s.ProtocolVersion, HealthCheck: s.HealthCheck, DeregistrationDelay: s.DeregistrationDelay, Stickiness: s.Stickiness, Targets: append([]Target(nil), s.Targets...), Tags: targetGroupManagedTags(s.Tags, s.ManagedKey)}
}

type tgSink struct{}

func (tgSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (tgSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }
func setupTG(t *testing.T, api TargetGroupAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	d := newGenericTargetGroupDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) TargetGroupAPI { return api })
	return restatetest.Start(t, restate.Reflect(d), restate.Reflect(tgSink{})).Ingress()
}
func tgSpec(n string) TargetGroupSpec {
	return applyDefaults(TargetGroupSpec{Account: "test", Region: "us-east-1", Name: n, Protocol: "HTTP", Port: 80, VpcId: "vpc-1", HealthCheck: HealthCheck{Protocol: "HTTP"}, Tags: map[string]string{"env": "test"}})
}
func TestGenericTGCoreLifecycle(t *testing.T) {
	api := newStatefulTGAPI()
	c := setupTG(t, api)
	s := tgSpec("core")
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[TargetGroupSpec, TargetGroupOutputs]{Client: c, ServiceName: ServiceName, Key: "us-east-1~core", Spec: s, Snapshot: api.snapshot, AssertInputs: func(t *testing.T, g TargetGroupSpec) { assert.Equal(t, "us-east-1~core", g.ManagedKey) }})
}
func TestGenericTGObservedImport(t *testing.T) {
	api := newStatefulTGAPI()
	api.seed(observedTG(tgSpec("imported")))
	c := setupTG(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[TargetGroupOutputs]{Client: c, ServiceName: ServiceName, Key: "us-east-1~imported", Ref: types.ImportRef{ResourceID: "imported", Account: "test"}, Snapshot: api.snapshot})
}
func TestGenericTGAmbiguousCreateRecovery(t *testing.T) {
	api := newStatefulTGAPI()
	api.failCreateOnce = true
	c := setupTG(t, api)
	out := provisionTG(t, c, "us-east-1~ambiguous", tgSpec("ambiguous"))
	assert.Equal(t, "ambiguous", out.TargetGroupName)
	assert.Equal(t, 1, api.snapshot().Creates)
}
func TestGenericTGUnownedCollision(t *testing.T) {
	api := newStatefulTGAPI()
	o := observedTG(tgSpec("collision"))
	delete(o.Tags, "praxis:managed-key")
	api.seed(o)
	c := setupTG(t, api)
	_, err := ingress.Object[TargetGroupSpec, TargetGroupOutputs](c, ServiceName, "us-east-1~collision", "Provision").Request(t.Context(), tgSpec("collision"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without exact Praxis ownership")
}
func TestGenericTGDrift(t *testing.T) {
	api := newStatefulTGAPI()
	c := setupTG(t, api)
	s := tgSpec("drift")
	key := "us-east-1~drift"
	provisionTG(t, c, key, s)
	api.force("drift", func(o *ObservedState) { o.DeregistrationDelay = 10; o.Tags["env"] = "old" })
	r, err := ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, r.Drift)
	assert.True(t, r.Correcting)
	assert.Equal(t, 300, api.get("drift").DeregistrationDelay)
}
func TestGenericTGImmutableAndExternalDelete(t *testing.T) {
	api := newStatefulTGAPI()
	c := setupTG(t, api)
	s := tgSpec("fixed")
	key := "us-east-1~fixed"
	provisionTG(t, c, key, s)
	s.Port = 81
	_, err := ingress.Object[TargetGroupSpec, TargetGroupOutputs](c, ServiceName, key, "Provision").Request(t.Context(), s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "immutable")
	api = newStatefulTGAPI()
	c = setupTG(t, api)
	s = tgSpec("gone")
	key = "us-east-1~gone"
	provisionTG(t, c, key, s)
	api.remove("gone")
	before := api.snapshot()
	r, err := ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, r.ReplacementRequired)
	assert.Equal(t, before.Creates, api.snapshot().Creates)
}
func provisionTG(t *testing.T, c *ingress.Client, key string, s TargetGroupSpec) TargetGroupOutputs {
	t.Helper()
	o, err := ingress.Object[TargetGroupSpec, TargetGroupOutputs](c, ServiceName, key, "Provision").Request(t.Context(), s)
	require.NoError(t, err)
	return o
}
