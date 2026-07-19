package natgw

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	"github.com/shirvan/praxis/internal/eventing"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type natDriftSink struct{}

func (natDriftSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (natDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

type genericNATAPI struct {
	mu sync.Mutex

	items                            map[string]ObservedState
	owners                           map[string]string
	creates, reads, updates, deletes int
	nextID                           int
	createState                      string
	createErrors                     []error
}

func newGenericNATAPI() *genericNATAPI {
	return &genericNATAPI{items: map[string]ObservedState{}, owners: map[string]string{}, createState: "available"}
}

func (f *genericNATAPI) CreateNATGateway(_ context.Context, spec NATGatewaySpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	f.nextID++
	id := fmt.Sprintf("nat-%03d", f.nextID)
	observed := ObservedState{
		NatGatewayId: id, SubnetId: spec.SubnetId, VpcId: "vpc-test",
		ConnectivityType: spec.ConnectivityType, State: f.createState,
		AllocationId: spec.AllocationId, PublicIp: "203.0.113.10",
		PrivateIp: "10.0.1.10", NetworkInterfaceId: "eni-test",
		Tags: maps.Clone(spec.Tags),
	}
	observed.Tags[managedKeyTag] = spec.ManagedKey
	f.items[id] = observed
	f.owners[spec.ManagedKey] = id
	if len(f.createErrors) > 0 {
		err := f.createErrors[0]
		f.createErrors = f.createErrors[1:]
		return "", err
	}
	return id, nil
}

func (f *genericNATAPI) DescribeNATGateway(_ context.Context, id string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	observed, ok := f.items[id]
	if !ok {
		return ObservedState{}, awserr.NotFound("NAT gateway not found")
	}
	observed.Tags = maps.Clone(observed.Tags)
	return observed, nil
}

func (f *genericNATAPI) DeleteNATGateway(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed, ok := f.items[id]
	if !ok {
		return awserr.NotFound("NAT gateway not found")
	}
	observed.State = "deleting"
	f.items[id] = observed
	f.deletes++
	return nil
}

func (f *genericNATAPI) WaitUntilAvailable(context.Context, string) error { return nil }

func (f *genericNATAPI) WaitUntilDeleted(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed, ok := f.items[id]
	if !ok {
		return awserr.NotFound("NAT gateway not found")
	}
	delete(f.items, id)
	delete(f.owners, observed.Tags[managedKeyTag])
	return nil
}

func (f *genericNATAPI) UpdateTags(_ context.Context, id string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed, ok := f.items[id]
	if !ok {
		return awserr.NotFound("NAT gateway not found")
	}
	owner := observed.Tags[managedKeyTag]
	observed.Tags = maps.Clone(tags)
	observed.Tags[managedKeyTag] = owner
	f.items[id] = observed
	f.updates++
	return nil
}

func (f *genericNATAPI) FindByManagedKey(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.owners[key], nil
}

func (f *genericNATAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *genericNATAPI) seed(observed ObservedState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed.Tags = maps.Clone(observed.Tags)
	f.items[observed.NatGatewayId] = observed
	if owner := observed.Tags[managedKeyTag]; owner != "" {
		f.owners[owner] = observed.NatGatewayId
	}
}

func (f *genericNATAPI) setState(id, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed := f.items[id]
	observed.State = state
	f.items[id] = observed
}

func (f *genericNATAPI) remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, id)
}

func setupGenericNAT(t *testing.T, api NATGatewayAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericNATGatewayDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) NATGatewayAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(natDriftSink{})).Ingress()
}

func managedNATSpec() NATGatewaySpec {
	return NATGatewaySpec{
		Account: "test", Region: "us-east-1", SubnetId: "subnet-test",
		ConnectivityType: "public", AllocationId: "eipalloc-test", Tags: map[string]string{"env": "test"},
	}
}

func TestGenericNATCoreLifecycle(t *testing.T) {
	api := newGenericNATAPI()
	client := setupGenericNAT(t, api)
	key := "us-east-1~generic-nat"
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[NATGatewaySpec, NATGatewayOutputs]{
		Client: client, ServiceName: ServiceName, Key: key, Spec: managedNATSpec(), Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs NATGatewaySpec) { assert.Equal(t, key, inputs.ManagedKey) },
	})
}

func TestGenericNATObservedImportLifecycle(t *testing.T) {
	api := newGenericNATAPI()
	api.seed(ObservedState{
		NatGatewayId: "nat-existing", SubnetId: "subnet-test", VpcId: "vpc-test",
		ConnectivityType: "public", AllocationId: "eipalloc-test", State: "available", Tags: map[string]string{},
	})
	client := setupGenericNAT(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[NATGatewayOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~existing-nat",
		Ref: types.ImportRef{Account: "test", ResourceID: "nat-existing"}, Snapshot: api.snapshot,
	})
}

func TestGenericNATPendingProgressesToReady(t *testing.T) {
	api := newGenericNATAPI()
	api.createState = "pending"
	client := setupGenericNAT(t, api)
	key := "us-east-1~pending-nat"
	outputs, err := ingress.Object[types.ProvisionRequest, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedNATSpec()))
	require.NoError(t, err)
	assert.Equal(t, "pending", outputs.State)
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusPending, status.Status)
	api.setState(outputs.NatGatewayId, "available")
	_, err = ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	status, err = ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
}

func TestGenericNATFailedRequiresDeleteThenProvision(t *testing.T) {
	api := newGenericNATAPI()
	api.createState = "failed"
	client := setupGenericNAT(t, api)
	key := "us-east-1~failed-nat"
	_, err := ingress.Object[types.ProvisionRequest, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedNATSpec()))
	require.Error(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
	_, err = ingress.Object[types.ProvisionRequest, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedNATSpec()))
	require.Error(t, err)
	assert.Equal(t, 1, api.snapshot().Creates, "failed gateways must never be auto-recreated")
	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Send(t.Context(), restate.Void{})
	require.NoError(t, err)
	api.createState = "available"
	_, err = ingress.Object[types.ProvisionRequest, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedNATSpec()))
	require.NoError(t, err)
	assert.Equal(t, 2, api.snapshot().Creates)
}

func TestGenericNATRecoversAmbiguousCreate(t *testing.T) {
	api := newGenericNATAPI()
	api.createErrors = []error{errors.New("create response lost")}
	client := setupGenericNAT(t, api)
	outputs, err := ingress.Object[types.ProvisionRequest, NATGatewayOutputs](client, ServiceName, "us-east-1~ambiguous-nat", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedNATSpec()))
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.NatGatewayId)
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericNATRejectsImmutablePlacement(t *testing.T) {
	api := newGenericNATAPI()
	client := setupGenericNAT(t, api)
	key := "us-east-1~immutable-nat"
	spec := managedNATSpec()
	_, err := ingress.Object[types.ProvisionRequest, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	spec.SubnetId = "subnet-other"
	_, err = ingress.Object[types.ProvisionRequest, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subnetId is immutable")
	stored, storedErr := ingress.Object[restate.Void, NATGatewaySpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, storedErr)
	assert.Equal(t, "subnet-test", stored.SubnetId)
}

func TestGenericNATProvisionChangeRejectsEveryImmutableField(t *testing.T) {
	previous := managedNATSpec()
	cases := map[string]func(*NATGatewaySpec){
		"account":      func(spec *NATGatewaySpec) { spec.Account = "other" },
		"region":       func(spec *NATGatewaySpec) { spec.Region = "us-west-2" },
		"subnet":       func(spec *NATGatewaySpec) { spec.SubnetId = "subnet-other" },
		"connectivity": func(spec *NATGatewaySpec) { spec.ConnectivityType = "private" },
		"allocation":   func(spec *NATGatewaySpec) { spec.AllocationId = "eipalloc-other" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			next := previous
			mutate(&next)
			_, err := (&genericOperations{}).ConvergeProvisionChange(nil, previous, next, ObservedState{}, NATGatewayOutputs{})
			require.Error(t, err)
			assert.EqualValues(t, 409, restate.ErrorCode(err))
		})
	}
}

func TestGenericNATExternalDeleteRequiresExplicitProvision(t *testing.T) {
	api := newGenericNATAPI()
	client := setupGenericNAT(t, api)
	key := "us-east-1~external-nat"
	outputs, err := ingress.Object[types.ProvisionRequest, NATGatewayOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedNATSpec()))
	require.NoError(t, err)
	before := api.snapshot()
	api.remove(outputs.NatGatewayId)
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Equal(t, before.Creates, api.snapshot().Creates)
}
