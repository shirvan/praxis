package alb

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

type statefulALBAPI struct {
	mu                               sync.Mutex
	resources                        map[string]ObservedState
	creates, reads, updates, deletes int
	failCreateOnce                   bool
}

func newStatefulALBAPI() *statefulALBAPI {
	return &statefulALBAPI{resources: map[string]ObservedState{}}
}

func (f *statefulALBAPI) CreateALB(_ context.Context, spec ALBSpec) (string, string, string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.resources[spec.Name]; exists {
		return "", "", "", "", &smithy.GenericAPIError{Code: "DuplicateLoadBalancerName"}
	}
	f.creates++
	observed := observedALB(spec)
	f.resources[spec.Name] = observed
	if f.failCreateOnce {
		f.failCreateOnce = false
		return "", "", "", "", errors.New("timeout after provider committed create")
	}
	return observed.LoadBalancerArn, observed.DnsName, observed.HostedZoneId, observed.VpcId, nil
}

func (f *statefulALBAPI) DescribeALB(_ context.Context, id string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	for name := range f.resources {
		observed := f.resources[name]
		if observed.Name == id || observed.LoadBalancerArn == id {
			return cloneALB(observed), nil
		}
	}
	return ObservedState{}, &smithy.GenericAPIError{Code: "LoadBalancerNotFound"}
}

func (f *statefulALBAPI) DeleteALB(_ context.Context, arn string) error {
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

func (f *statefulALBAPI) SetSubnets(_ context.Context, arn string, mappings []SubnetMapping) error {
	return f.mutate(arn, func(o *ObservedState) { o.Subnets = normalizeSubnets(mappings) })
}
func (f *statefulALBAPI) SetSecurityGroups(_ context.Context, arn string, groups []string) error {
	return f.mutate(arn, func(o *ObservedState) { o.SecurityGroups = sortedCopy(groups) })
}
func (f *statefulALBAPI) SetIpAddressType(_ context.Context, arn, value string) error {
	return f.mutate(arn, func(o *ObservedState) { o.IpAddressType = value })
}
func (f *statefulALBAPI) ModifyAttributes(_ context.Context, arn string, attrs map[string]string) error {
	return f.mutate(arn, func(o *ObservedState) {
		if value, ok := attrs["deletion_protection.enabled"]; ok {
			o.DeletionProtection = value == "true"
		}
		if value, ok := attrs["idle_timeout.timeout_seconds"]; ok && value == "60" {
			o.IdleTimeout = 60
		}
	})
}
func (f *statefulALBAPI) UpdateTags(_ context.Context, arn string, tags map[string]string) error {
	return f.mutate(arn, func(o *ObservedState) { o.Tags = maps.Clone(tags) })
}
func (f *statefulALBAPI) mutate(arn string, fn func(*ObservedState)) error {
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
func (f *statefulALBAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}
func (f *statefulALBAPI) seed(observed ObservedState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resources[observed.Name] = cloneALB(observed)
}
func (f *statefulALBAPI) remove(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.resources, name)
}
func (f *statefulALBAPI) force(name string, fn func(*ObservedState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed := f.resources[name]
	fn(&observed)
	f.resources[name] = observed
}
func (f *statefulALBAPI) get(name string) ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneALB(f.resources[name])
}

func observedALB(spec ALBSpec) ObservedState {
	return ObservedState{LoadBalancerArn: "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/" + spec.Name + "/1", DnsName: spec.Name + ".elb.test", HostedZoneId: "ZTEST", Name: spec.Name, Scheme: spec.Scheme, VpcId: "vpc-1", IpAddressType: spec.IpAddressType, Subnets: resolveSubnets(spec), SecurityGroups: sortedCopy(spec.SecurityGroups), AccessLogs: spec.AccessLogs, DeletionProtection: spec.DeletionProtection, IdleTimeout: spec.IdleTimeout, Tags: albManagedTags(spec.Tags, spec.ManagedKey), State: "active"}
}
func cloneALB(observed ObservedState) ObservedState {
	observed.Tags = maps.Clone(observed.Tags)
	observed.Subnets = append([]string(nil), observed.Subnets...)
	observed.SecurityGroups = append([]string(nil), observed.SecurityGroups...)
	return observed
}

type albSink struct{}

func (albSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (albSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

func setupGenericALB(t *testing.T, api ALBAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericALBDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) ALBAPI { return api })
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(albSink{})).Ingress()
}

func genericALBSpec(name string) ALBSpec {
	return applyDefaults(ALBSpec{Account: "test", Region: "us-east-1", Name: name, Subnets: []string{"subnet-a", "subnet-b"}, SecurityGroups: []string{"sg-1"}, Tags: map[string]string{"env": "test"}})
}

func TestGenericALBCoreLifecycle(t *testing.T) {
	api := newStatefulALBAPI()
	client := setupGenericALB(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[ALBSpec, ALBOutputs]{Client: client, ServiceName: ServiceName, Key: "us-east-1~core", Spec: genericALBSpec("core"), Snapshot: api.snapshot, AssertInputs: func(t *testing.T, got ALBSpec) { assert.Equal(t, "us-east-1~core", got.ManagedKey) }})
}

func TestGenericALBObservedImport(t *testing.T) {
	api := newStatefulALBAPI()
	api.seed(observedALB(genericALBSpec("imported")))
	client := setupGenericALB(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[ALBOutputs]{Client: client, ServiceName: ServiceName, Key: "us-east-1~imported", Ref: types.ImportRef{ResourceID: "imported", Account: "test"}, Snapshot: api.snapshot})
}

func TestGenericALBAmbiguousCreateRecovery(t *testing.T) {
	api := newStatefulALBAPI()
	api.failCreateOnce = true
	client := setupGenericALB(t, api)
	out := provisionALB(t, client, "us-east-1~ambiguous", genericALBSpec("ambiguous"))
	assert.NotEmpty(t, out.LoadBalancerArn)
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericALBRejectsUnownedCollision(t *testing.T) {
	api := newStatefulALBAPI()
	observed := observedALB(genericALBSpec("collision"))
	delete(observed.Tags, managedKeyTag)
	api.seed(observed)
	client := setupGenericALB(t, api)
	_, err := ingress.Object[ALBSpec, ALBOutputs](client, ServiceName, "us-east-1~collision", "Provision").Request(t.Context(), genericALBSpec("collision"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without exact Praxis ownership")
}

func TestGenericALBCorrectsDrift(t *testing.T) {
	api := newStatefulALBAPI()
	client := setupGenericALB(t, api)
	spec := genericALBSpec("drift")
	key := "us-east-1~drift"
	provisionALB(t, client, key, spec)
	api.force("drift", func(o *ObservedState) { o.IpAddressType = "dualstack"; o.Tags["env"] = "old" })
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Equal(t, "ipv4", api.get("drift").IpAddressType)
	assert.Equal(t, "test", api.get("drift").Tags["env"])
}

func TestGenericALBImmutableAndExternalDelete(t *testing.T) {
	api := newStatefulALBAPI()
	client := setupGenericALB(t, api)
	spec := genericALBSpec("fixed")
	key := "us-east-1~fixed"
	provisionALB(t, client, key, spec)
	spec.Scheme = "internal"
	_, err := ingress.Object[ALBSpec, ALBOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "immutable")

	api = newStatefulALBAPI()
	client = setupGenericALB(t, api)
	spec = genericALBSpec("gone")
	key = "us-east-1~gone"
	provisionALB(t, client, key, spec)
	api.remove("gone")
	before := api.snapshot()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Equal(t, before.Creates, api.snapshot().Creates)
}

func provisionALB(t *testing.T, client *ingress.Client, key string, spec ALBSpec) ALBOutputs {
	t.Helper()
	out, err := ingress.Object[ALBSpec, ALBOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	return out
}
