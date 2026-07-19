package listener

import (
	"context"
	"errors"
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
	"maps"
	"strconv"
	"sync"
	"testing"
)

type statefulListenerAPI struct {
	mu                               sync.Mutex
	items                            map[string]ObservedState
	creates, reads, updates, deletes int
	fail                             bool
}

func newListenerAPI() *statefulListenerAPI {
	return &statefulListenerAPI{items: map[string]ObservedState{}}
}
func (f *statefulListenerAPI) CreateListener(_ context.Context, s ListenerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for arn := range f.items {
		o := f.items[arn]
		if o.LoadBalancerArn == s.LoadBalancerArn && o.Port == s.Port {
			return "", &smithy.GenericAPIError{Code: "DuplicateListener"}
		}
	}
	f.creates++
	arn := "arn:listener/" + s.LoadBalancerArn + strconv.Itoa(s.Port)
	f.items[arn] = obsListener(arn, s)
	if f.fail {
		f.fail = false
		return "", errors.New("timeout after create")
	}
	return arn, nil
}
func (f *statefulListenerAPI) DescribeListener(_ context.Context, a string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	o, ok := f.items[a]
	if !ok {
		return ObservedState{}, listenerNotFoundError()
	}
	return cloneListener(o), nil
}
func (f *statefulListenerAPI) FindListenerByPort(_ context.Context, lb string, p int) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	for arn := range f.items {
		o := f.items[arn]
		if o.LoadBalancerArn == lb && o.Port == p {
			return cloneListener(o), nil
		}
	}
	return ObservedState{}, listenerNotFoundError()
}
func (f *statefulListenerAPI) DeleteListener(_ context.Context, a string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[a]; !ok {
		return listenerNotFoundError()
	}
	f.deletes++
	delete(f.items, a)
	return nil
}
func (f *statefulListenerAPI) ModifyListener(_ context.Context, a string, s ListenerSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.items[a]
	if !ok {
		return listenerNotFoundError()
	}
	f.updates++
	tags := o.Tags
	o = obsListener(a, s)
	o.Tags = tags
	f.items[a] = o
	return nil
}
func (f *statefulListenerAPI) UpdateTags(_ context.Context, a string, t map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.items[a]
	if !ok {
		return listenerNotFoundError()
	}
	f.updates++
	o.Tags = maps.Clone(t)
	f.items[a] = o
	return nil
}
func (f *statefulListenerAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}
func (f *statefulListenerAPI) seed(o ObservedState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[o.ListenerArn] = cloneListener(o)
}
func (f *statefulListenerAPI) remove(a string) { f.mu.Lock(); defer f.mu.Unlock(); delete(f.items, a) }
func (f *statefulListenerAPI) force(a string, fn func(*ObservedState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.items[a]
	fn(&o)
	f.items[a] = o
}
func cloneListener(o ObservedState) ObservedState {
	o.Tags = maps.Clone(o.Tags)
	o.DefaultActions = append([]ListenerAction(nil), o.DefaultActions...)
	return o
}
func obsListener(a string, s ListenerSpec) ObservedState {
	return ObservedState{ListenerArn: a, LoadBalancerArn: s.LoadBalancerArn, Port: s.Port, Protocol: s.Protocol, SslPolicy: effectiveSslPolicy(s.SslPolicy, s.Protocol), CertificateArn: s.CertificateArn, AlpnPolicy: s.AlpnPolicy, DefaultActions: append([]ListenerAction(nil), s.DefaultActions...), Tags: listenerManagedTags(s.Tags, s.ManagedKey)}
}
func listenerNotFoundError() error {
	return &smithy.GenericAPIError{Code: "ListenerNotFound", Message: "not found"}
}

type listenerSink struct{}

func (listenerSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (listenerSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }
func setupListener(t *testing.T, a ListenerAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	d := newGenericListenerDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) ListenerAPI { return a })
	return restatetest.Start(t, restate.Reflect(d), restate.Reflect(listenerSink{})).Ingress()
}
func listenerSpec(p int) ListenerSpec {
	return ListenerSpec{Account: "test", Region: "us-east-1", LoadBalancerArn: "lb-1", Port: p, Protocol: "HTTP", DefaultActions: []ListenerAction{{Type: "forward", TargetGroupArn: "tg-1"}}, Tags: map[string]string{"env": "test"}}
}
func TestGenericListenerCore(t *testing.T) {
	a := newListenerAPI()
	c := setupListener(t, a)
	s := listenerSpec(80)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[ListenerSpec, ListenerOutputs]{Client: c, ServiceName: ServiceName, Key: "us-east-1~web", Spec: s, Snapshot: a.snapshot, AssertInputs: func(t *testing.T, g ListenerSpec) { assert.Equal(t, "us-east-1~web", g.ManagedKey) }})
}
func TestGenericListenerImport(t *testing.T) {
	a := newListenerAPI()
	s := listenerSpec(81)
	s.ManagedKey = "other"
	a.seed(obsListener("arn:listener/import", s))
	c := setupListener(t, a)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[ListenerOutputs]{Client: c, ServiceName: ServiceName, Key: "us-east-1~import", Ref: types.ImportRef{ResourceID: "arn:listener/import", Account: "test"}, Snapshot: a.snapshot})
}
func TestGenericListenerReplayAndCollision(t *testing.T) {
	a := newListenerAPI()
	a.fail = true
	c := setupListener(t, a)
	o := provisionListener(t, c, "us-east-1~replay", listenerSpec(82))
	assert.NotEmpty(t, o.ListenerArn)
	assert.Equal(t, 1, a.snapshot().Creates)
	a = newListenerAPI()
	s := listenerSpec(83)
	a.seed(obsListener("arn:listener/collision", s))
	c = setupListener(t, a)
	_, e := ingress.Object[ListenerSpec, ListenerOutputs](c, ServiceName, "us-east-1~collision", "Provision").Request(t.Context(), s)
	require.Error(t, e)
	assert.Contains(t, e.Error(), "exact Praxis ownership")
}
func TestGenericListenerDriftImmutableExternal(t *testing.T) {
	a := newListenerAPI()
	c := setupListener(t, a)
	s := listenerSpec(84)
	key := "us-east-1~x"
	o := provisionListener(t, c, key, s)
	a.force(o.ListenerArn, func(x *ObservedState) { x.Protocol = "HTTPS" })
	r, e := ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, e)
	assert.True(t, r.Drift)
	assert.True(t, r.Correcting)
	s.LoadBalancerArn = "lb-2"
	_, e = ingress.Object[ListenerSpec, ListenerOutputs](c, ServiceName, key, "Provision").Request(t.Context(), s)
	require.Error(t, e)
	assert.Contains(t, e.Error(), "immutable")
	a = newListenerAPI()
	c = setupListener(t, a)
	s = listenerSpec(85)
	key = "us-east-1~gone"
	o = provisionListener(t, c, key, s)
	a.remove(o.ListenerArn)
	r, e = ingress.Object[restate.Void, types.ReconcileResult](c, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, e)
	assert.True(t, r.ReplacementRequired)
}
func provisionListener(t *testing.T, c *ingress.Client, k string, s ListenerSpec) ListenerOutputs {
	t.Helper()
	o, e := ingress.Object[ListenerSpec, ListenerOutputs](c, ServiceName, k, "Provision").Request(t.Context(), s)
	require.NoError(t, e)
	return o
}
