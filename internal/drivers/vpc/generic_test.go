package vpc

import (
	"context"
	"errors"
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

type statefulVPCAPI struct {
	mu sync.Mutex

	exists     bool
	managedKey string
	observed   ObservedState
	nextID     int

	creates int
	reads   int
	updates int
	deletes int

	createCalls           int
	waitCalls             int
	createAfterApplyError []error
	waitErrors            []error
	deleteErrors          []error
	operations            []string
}

type vpcDriftSink struct{}

func (vpcDriftSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (vpcDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

func (f *statefulVPCAPI) CreateVpc(_ context.Context, spec VPCSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	if f.exists {
		return "", &mockAPIError{code: "VpcLimitExceeded", message: "unexpected duplicate VPC create"}
	}
	f.nextID++
	vpcID := "vpc-created"
	if f.nextID > 1 {
		vpcID += "-duplicate"
	}
	tags := maps.Clone(spec.Tags)
	if tags == nil {
		tags = map[string]string{}
	}
	tags["praxis:managed-key"] = spec.ManagedKey
	tenancy := spec.InstanceTenancy
	if tenancy == "" {
		tenancy = "default"
	}
	f.exists = true
	f.managedKey = spec.ManagedKey
	f.observed = ObservedState{
		VpcId: vpcID, CidrBlock: spec.CidrBlock, State: "pending",
		EnableDnsSupport: true, EnableDnsHostnames: false, InstanceTenancy: tenancy,
		OwnerId: "123456789012", DhcpOptionsId: "dopt-123", Tags: tags,
	}
	f.creates++
	f.operations = append(f.operations, "create")
	if len(f.createAfterApplyError) > 0 {
		err := f.createAfterApplyError[0]
		f.createAfterApplyError = f.createAfterApplyError[1:]
		return "", err
	}
	return vpcID, nil
}

func (f *statefulVPCAPI) DescribeVpc(_ context.Context, vpcID string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if !f.exists || f.observed.VpcId != vpcID {
		return ObservedState{}, awserr.NotFound("VPC " + vpcID + " not found")
	}
	return cloneVPCObserved(f.observed), nil
}

func (f *statefulVPCAPI) DeleteVpc(_ context.Context, vpcID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deleteErrors) > 0 {
		err := f.deleteErrors[0]
		f.deleteErrors = f.deleteErrors[1:]
		return err
	}
	if !f.exists || f.observed.VpcId != vpcID {
		return awserr.NotFound("VPC " + vpcID + " not found")
	}
	f.exists = false
	f.managedKey = ""
	f.observed = ObservedState{}
	f.deletes++
	f.operations = append(f.operations, "delete")
	return nil
}

func (f *statefulVPCAPI) WaitUntilAvailable(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waitCalls++
	if len(f.waitErrors) > 0 {
		err := f.waitErrors[0]
		f.waitErrors = f.waitErrors[1:]
		return err
	}
	f.observed.State = "available"
	f.operations = append(f.operations, "wait")
	return nil
}

func (f *statefulVPCAPI) ModifyDnsHostnames(_ context.Context, _ string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.EnableDnsHostnames = enabled
	f.operations = append(f.operations, operationValue("hostnames", enabled))
	return nil
}

func (f *statefulVPCAPI) ModifyDnsSupport(_ context.Context, _ string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.EnableDnsSupport = enabled
	f.operations = append(f.operations, operationValue("support", enabled))
	return nil
}

func (f *statefulVPCAPI) UpdateTags(_ context.Context, _ string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	managedKey := f.observed.Tags["praxis:managed-key"]
	f.observed.Tags = maps.Clone(tags)
	if f.observed.Tags == nil {
		f.observed.Tags = map[string]string{}
	}
	if managedKey != "" {
		f.observed.Tags["praxis:managed-key"] = managedKey
	}
	f.operations = append(f.operations, "tags")
	return nil
}

func (f *statefulVPCAPI) FindByManagedKey(_ context.Context, managedKey string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.exists && f.managedKey == managedKey {
		return f.observed.VpcId, nil
	}
	return "", nil
}

func (f *statefulVPCAPI) FindByTags(context.Context, map[string]string) (string, error) {
	return "", nil
}

func (f *statefulVPCAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

type vpcTestState struct {
	Exists      bool
	Observed    ObservedState
	CreateCalls int
	WaitCalls   int
	Operations  []string
}

func (f *statefulVPCAPI) current() vpcTestState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return vpcTestState{
		Exists: f.exists, Observed: cloneVPCObserved(f.observed),
		CreateCalls: f.createCalls, WaitCalls: f.waitCalls,
		Operations: append([]string(nil), f.operations...),
	}
}

func (f *statefulVPCAPI) removeExternally() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exists = false
	f.observed = ObservedState{}
}

func cloneVPCObserved(observed ObservedState) ObservedState {
	observed.Tags = maps.Clone(observed.Tags)
	return observed
}

func operationValue(name string, enabled bool) string {
	if enabled {
		return name + ":true"
	}
	return name + ":false"
}

func setupGenericVPC(t *testing.T, api VPCAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericVPCDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) VPCAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(vpcDriftSink{})).Ingress()
}

func managedVPCSpec() VPCSpec {
	return VPCSpec{
		Account: "test", Region: "us-east-1", CidrBlock: "10.42.0.0/16",
		EnableDnsHostnames: true, EnableDnsSupport: true,
		Tags: map[string]string{"Name": "generic-vpc", "env": "test"},
	}
}

func existingVPC(vpcID, managedKey string) *statefulVPCAPI {
	return &statefulVPCAPI{
		exists: true, managedKey: managedKey,
		observed: ObservedState{
			VpcId: vpcID, CidrBlock: "10.99.0.0/16", State: "available",
			EnableDnsHostnames: false, EnableDnsSupport: true, InstanceTenancy: "default",
			OwnerId: "123456789012", DhcpOptionsId: "dopt-123",
			Tags: map[string]string{"Name": "existing-vpc", "praxis:managed-key": managedKey},
		},
	}
}

func TestGenericVPCServiceName(t *testing.T) {
	assert.Equal(t, ServiceName, NewGenericVPCDriver(nil).ServiceName())
}

func TestGenericVPCCoreLifecycleAndLateInitialization(t *testing.T) {
	api := &statefulVPCAPI{}
	client := setupGenericVPC(t, api)
	key := "us-east-1~generic-vpc"
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[VPCSpec, VPCOutputs]{
		Client: client, ServiceName: ServiceName, Key: key, Spec: managedVPCSpec(), Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs VPCSpec) {
			assert.Equal(t, key, inputs.ManagedKey)
			assert.Equal(t, "default", inputs.InstanceTenancy)
		},
	})
}

func TestGenericVPCObservedImportLifecycle(t *testing.T) {
	api := existingVPC("vpc-existing", "old-owner")
	client := setupGenericVPC(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[VPCOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~vpc-existing",
		Ref: types.ImportRef{ResourceID: "vpc-existing", Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericVPCRecoversAmbiguousCreateWithoutDuplicate(t *testing.T) {
	api := &statefulVPCAPI{createAfterApplyError: []error{errors.New("ServiceUnavailable: response lost after CreateVpc")}}
	client := setupGenericVPC(t, api)
	outputs, err := ingress.Object[types.ProvisionRequest, VPCOutputs](client, ServiceName, "us-east-1~ambiguous", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedVPCSpec()))
	require.NoError(t, err)
	assert.Equal(t, "vpc-created", outputs.VpcId)
	state := api.current()
	assert.Equal(t, 1, state.CreateCalls)
	assert.True(t, state.Exists)
}

func TestGenericVPCWaiterRetriesWithoutSecondCreate(t *testing.T) {
	api := &statefulVPCAPI{waitErrors: []error{errors.New("RequestLimitExceeded: transient waiter failure")}}
	client := setupGenericVPC(t, api)
	_, err := ingress.Object[types.ProvisionRequest, VPCOutputs](client, ServiceName, "us-east-1~wait", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedVPCSpec()))
	require.NoError(t, err)
	state := api.current()
	assert.Equal(t, 1, state.CreateCalls)
	assert.Equal(t, 2, state.WaitCalls)
}

func TestGenericVPCPreservesDNSDependencyOrdering(t *testing.T) {
	api := &statefulVPCAPI{}
	client := setupGenericVPC(t, api)
	key := "us-east-1~dns-order"
	spec := managedVPCSpec()
	spec.EnableDnsHostnames = false
	spec.EnableDnsSupport = false
	_, err := ingress.Object[types.ProvisionRequest, VPCOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)

	before := len(api.current().Operations)
	spec.EnableDnsSupport = true
	spec.EnableDnsHostnames = true
	_, err = ingress.Object[types.ProvisionRequest, VPCOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	enabled := api.current().Operations[before:]
	assert.Equal(t, []string{"support:true", "hostnames:true"}, enabled)

	before = len(api.current().Operations)
	spec.EnableDnsHostnames = false
	spec.EnableDnsSupport = false
	_, err = ingress.Object[types.ProvisionRequest, VPCOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	disabled := api.current().Operations[before:]
	assert.Equal(t, []string{"hostnames:false", "support:false"}, disabled)
}

func TestGenericVPCManagedReconcileCorrectsDNSAndTags(t *testing.T) {
	api := &statefulVPCAPI{}
	client := setupGenericVPC(t, api)
	key := "us-east-1~drift"
	spec := managedVPCSpec()
	_, err := ingress.Object[types.ProvisionRequest, VPCOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	api.mu.Lock()
	api.observed.EnableDnsHostnames = false
	api.observed.EnableDnsSupport = false
	api.observed.Tags = map[string]string{"Name": "drift", "stale": "remove", "praxis:managed-key": key}
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	observed := api.current().Observed
	assert.True(t, observed.EnableDnsSupport)
	assert.True(t, observed.EnableDnsHostnames)
	assert.Equal(t, map[string]string{"Name": "generic-vpc", "env": "test", "praxis:managed-key": key}, observed.Tags)
}

func TestGenericVPCRejectsImmutableFieldsAndRetainsInputs(t *testing.T) {
	api := &statefulVPCAPI{}
	client := setupGenericVPC(t, api)
	key := "us-east-1~immutable"
	spec := managedVPCSpec()
	_, err := ingress.Object[types.ProvisionRequest, VPCOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	accepted, err := ingress.Object[restate.Void, VPCSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	tests := []struct {
		field  string
		mutate func(*VPCSpec)
	}{
		{field: "cidrBlock", mutate: func(s *VPCSpec) { s.CidrBlock = "10.77.0.0/16" }},
		{field: "instanceTenancy", mutate: func(s *VPCSpec) { s.InstanceTenancy = "dedicated" }},
	}
	for _, tt := range tests {
		changed := accepted
		tt.mutate(&changed)
		_, err = ingress.Object[types.ProvisionRequest, VPCOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, changed))
		require.Error(t, err)
		assert.Contains(t, err.Error(), tt.field+" is immutable")
		retained, getErr := ingress.Object[restate.Void, VPCSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
		require.NoError(t, getErr)
		assert.Equal(t, accepted, retained)
	}
}

func TestGenericVPCOwnershipConflictIsRejected(t *testing.T) {
	key := "us-east-1~conflict"
	api := existingVPC("vpc-conflict", key)
	client := setupGenericVPC(t, api)
	_, err := ingress.Object[types.ProvisionRequest, VPCOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedVPCSpec()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already managed by Praxis")
	assert.Zero(t, api.snapshot().Creates)
}

func TestGenericVPCDeleteRetriesDependencyViolation(t *testing.T) {
	api := existingVPC("vpc-dependent", "old-owner")
	api.deleteErrors = []error{&mockAPIError{code: "DependencyViolation", message: "subnet is attached"}}
	client := setupGenericVPC(t, api)
	key := "us-east-1~vpc-dependent"
	_, err := ingress.Object[types.ImportRef, VPCOutputs](client, ServiceName, key, "Import").Request(
		t.Context(), types.ImportRef{ResourceID: "vpc-dependent", Mode: types.ModeManaged, Account: "test"},
	)
	require.NoError(t, err)
	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.False(t, api.current().Exists)
	assert.Equal(t, 1, api.snapshot().Deletes)
}

func TestGenericVPCDefaultVPCDeleteIsBlockedBeforeProviderCall(t *testing.T) {
	api := existingVPC("vpc-default", "old-owner")
	api.observed.IsDefault = true
	client := setupGenericVPC(t, api)
	key := "us-east-1~vpc-default"
	_, err := ingress.Object[types.ImportRef, VPCOutputs](client, ServiceName, key, "Import").Request(
		t.Context(), types.ImportRef{ResourceID: "vpc-default", Mode: types.ModeManaged, Account: "test"},
	)
	require.NoError(t, err)
	before := api.snapshot()
	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "default VPC")
	assert.Equal(t, before, api.snapshot())
}

func TestGenericVPCExternalDeleteRequiresReplacementWithoutCreate(t *testing.T) {
	api := &statefulVPCAPI{}
	client := setupGenericVPC(t, api)
	key := "us-east-1~external-delete"
	_, err := ingress.Object[types.ProvisionRequest, VPCOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedVPCSpec()))
	require.NoError(t, err)
	creates := api.snapshot().Creates
	api.removeExternally()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "VPC resource was deleted externally")
	assert.Equal(t, creates, api.snapshot().Creates)
}

func TestGenericVPCImportRejectsMissingResource(t *testing.T) {
	api := &statefulVPCAPI{}
	client := setupGenericVPC(t, api)
	_, err := ingress.Object[types.ImportRef, VPCOutputs](client, ServiceName, "us-east-1~missing", "Import").Request(
		t.Context(), types.ImportRef{ResourceID: "vpc-missing", Account: "test"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
	assert.Zero(t, api.snapshot().Creates)
}
