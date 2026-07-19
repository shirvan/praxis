package nlb

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

type statefulNLBAPI struct {
	mu                               sync.Mutex
	resources                        map[string]ObservedState
	creates, reads, updates, deletes int
	failCreateOnce                   bool
}

func newStatefulNLBAPI() *statefulNLBAPI {
	return &statefulNLBAPI{resources: map[string]ObservedState{}}
}
func (f *statefulNLBAPI) CreateNLB(_ context.Context, spec NLBSpec) (string, string, string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.resources[spec.Name]; ok {
		return "", "", "", "", &smithy.GenericAPIError{Code: "DuplicateLoadBalancerName"}
	}
	f.creates++
	observed := observedNLB(spec)
	f.resources[spec.Name] = observed
	if f.failCreateOnce {
		f.failCreateOnce = false
		return "", "", "", "", errors.New("timeout after provider committed create")
	}
	return observed.LoadBalancerArn, observed.DnsName, observed.HostedZoneId, observed.VpcId, nil
}
func (f *statefulNLBAPI) DescribeNLB(_ context.Context, id string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	for name := range f.resources {
		observed := f.resources[name]
		if observed.Name == id || observed.LoadBalancerArn == id {
			return cloneNLB(observed), nil
		}
	}
	return ObservedState{}, &smithy.GenericAPIError{Code: "LoadBalancerNotFound"}
}
func (f *statefulNLBAPI) DeleteNLB(_ context.Context, arn string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name := range f.resources {
		observed := f.resources[name]
		if observed.LoadBalancerArn == arn {
			f.deletes++
			delete(f.resources, name)
			return nil
		}
	}
	return &smithy.GenericAPIError{Code: "LoadBalancerNotFound"}
}
func (f *statefulNLBAPI) SetSubnets(_ context.Context, arn string, mappings []SubnetMapping) error {
	return f.mutate(arn, func(o *ObservedState) { o.Subnets = resolveSubnets(NLBSpec{SubnetMappings: mappings}) })
}
func (f *statefulNLBAPI) SetIpAddressType(_ context.Context, arn, value string) error {
	return f.mutate(arn, func(o *ObservedState) { o.IpAddressType = value })
}
func (f *statefulNLBAPI) ModifyAttributes(_ context.Context, arn string, attrs map[string]string) error {
	return f.mutate(arn, func(o *ObservedState) {
		if value, ok := attrs["deletion_protection.enabled"]; ok {
			o.DeletionProtection = value == "true"
		}
		if value, ok := attrs["load_balancing.cross_zone.enabled"]; ok {
			o.CrossZoneLoadBalancing = value == "true"
		}
	})
}
func (f *statefulNLBAPI) UpdateTags(_ context.Context, arn string, tags map[string]string) error {
	return f.mutate(arn, func(o *ObservedState) { o.Tags = maps.Clone(tags) })
}
func (f *statefulNLBAPI) mutate(arn string, fn func(*ObservedState)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name := range f.resources {
		observed := f.resources[name]
		if observed.LoadBalancerArn == arn {
			f.updates++
			fn(&observed)
			f.resources[name] = observed
			return nil
		}
	}
	return &smithy.GenericAPIError{Code: "LoadBalancerNotFound"}
}
func (f *statefulNLBAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}
func (f *statefulNLBAPI) seed(observed ObservedState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resources[observed.Name] = cloneNLB(observed)
}
func (f *statefulNLBAPI) remove(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.resources, name)
}
func (f *statefulNLBAPI) force(name string, fn func(*ObservedState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed := f.resources[name]
	fn(&observed)
	f.resources[name] = observed
}
func (f *statefulNLBAPI) get(name string) ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneNLB(f.resources[name])
}
func observedNLB(spec NLBSpec) ObservedState {
	return ObservedState{LoadBalancerArn: "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/net/" + spec.Name + "/1", DnsName: spec.Name + ".elb.test", HostedZoneId: "ZTEST", Name: spec.Name, Scheme: spec.Scheme, VpcId: "vpc-1", IpAddressType: spec.IpAddressType, Subnets: resolveSubnets(spec), CrossZoneLoadBalancing: spec.CrossZoneLoadBalancing, DeletionProtection: spec.DeletionProtection, Tags: nlbManagedTags(spec.Tags, spec.ManagedKey), State: "active"}
}
func cloneNLB(observed ObservedState) ObservedState {
	observed.Tags = maps.Clone(observed.Tags)
	observed.Subnets = append([]string(nil), observed.Subnets...)
	return observed
}

type nlbSink struct{}

func (nlbSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (nlbSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }
func setupGenericNLB(t *testing.T, api NLBAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericNLBDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) NLBAPI { return api })
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(nlbSink{})).Ingress()
}
func genericNLBSpec(name string) NLBSpec {
	return applyDefaults(NLBSpec{Account: "test", Region: "us-east-1", Name: name, Subnets: []string{"subnet-a"}, Tags: map[string]string{"env": "test"}})
}

func TestGenericNLBCoreLifecycle(t *testing.T) {
	api := newStatefulNLBAPI()
	client := setupGenericNLB(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[NLBSpec, NLBOutputs]{Client: client, ServiceName: ServiceName, Key: "us-east-1~core", Spec: genericNLBSpec("core"), Snapshot: api.snapshot, AssertInputs: func(t *testing.T, got NLBSpec) { assert.Equal(t, "us-east-1~core", got.ManagedKey) }})
}
func TestGenericNLBObservedImport(t *testing.T) {
	api := newStatefulNLBAPI()
	api.seed(observedNLB(genericNLBSpec("imported")))
	client := setupGenericNLB(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[NLBOutputs]{Client: client, ServiceName: ServiceName, Key: "us-east-1~imported", Ref: types.ImportRef{ResourceID: "imported", Account: "test"}, Snapshot: api.snapshot})
}
func TestGenericNLBAmbiguousCreateRecovery(t *testing.T) {
	api := newStatefulNLBAPI()
	api.failCreateOnce = true
	client := setupGenericNLB(t, api)
	out := provisionNLB(t, client, "us-east-1~ambiguous", genericNLBSpec("ambiguous"))
	assert.NotEmpty(t, out.LoadBalancerArn)
	assert.Equal(t, 1, api.snapshot().Creates)
}
func TestGenericNLBRejectsUnownedCollision(t *testing.T) {
	api := newStatefulNLBAPI()
	observed := observedNLB(genericNLBSpec("collision"))
	delete(observed.Tags, managedKeyTag)
	api.seed(observed)
	client := setupGenericNLB(t, api)
	_, err := ingress.Object[NLBSpec, NLBOutputs](client, ServiceName, "us-east-1~collision", "Provision").Request(t.Context(), genericNLBSpec("collision"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without exact Praxis ownership")
}
func TestGenericNLBCorrectsDrift(t *testing.T) {
	api := newStatefulNLBAPI()
	client := setupGenericNLB(t, api)
	spec := genericNLBSpec("drift")
	key := "us-east-1~drift"
	provisionNLB(t, client, key, spec)
	api.force("drift", func(o *ObservedState) { o.CrossZoneLoadBalancing = true; o.Tags["env"] = "old" })
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.False(t, api.get("drift").CrossZoneLoadBalancing)
	assert.Equal(t, "test", api.get("drift").Tags["env"])
}
func TestGenericNLBImmutableAndExternalDelete(t *testing.T) {
	api := newStatefulNLBAPI()
	client := setupGenericNLB(t, api)
	spec := genericNLBSpec("fixed")
	key := "us-east-1~fixed"
	provisionNLB(t, client, key, spec)
	spec.Scheme = "internal"
	_, err := ingress.Object[NLBSpec, NLBOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "immutable")
	api = newStatefulNLBAPI()
	client = setupGenericNLB(t, api)
	spec = genericNLBSpec("gone")
	key = "us-east-1~gone"
	provisionNLB(t, client, key, spec)
	api.remove("gone")
	before := api.snapshot()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Equal(t, before.Creates, api.snapshot().Creates)
}
func provisionNLB(t *testing.T, client *ingress.Client, key string, spec NLBSpec) NLBOutputs {
	t.Helper()
	out, err := ingress.Object[NLBSpec, NLBOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	return out
}
