package vpcpeering

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

type peeringDriftSink struct{}

func (peeringDriftSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (peeringDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

type statefulPeeringAPI struct {
	mu                               sync.Mutex
	items                            map[string]ObservedState
	owners                           map[string]string
	creates, reads, updates, deletes int
	nextID                           int
	createStatus                     string
	createErrors                     []error
}

func newStatefulPeeringAPI() *statefulPeeringAPI {
	return &statefulPeeringAPI{items: map[string]ObservedState{}, owners: map[string]string{}, createStatus: "pending-acceptance"}
}

func (f *statefulPeeringAPI) CreateVPCPeeringConnection(_ context.Context, spec VPCPeeringSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	f.nextID++
	id := fmt.Sprintf("pcx-%03d", f.nextID)
	tags := maps.Clone(spec.Tags)
	tags[managedKeyTag] = spec.ManagedKey
	f.items[id] = ObservedState{
		VpcPeeringConnectionId: id, RequesterVpcId: spec.RequesterVpcId, AccepterVpcId: spec.AccepterVpcId,
		RequesterCidrBlock: "10.0.0.0/16", AccepterCidrBlock: "10.1.0.0/16",
		RequesterOwnerId: "123456789012", AccepterOwnerId: "123456789012",
		Status: f.createStatus, Tags: tags,
	}
	f.owners[spec.ManagedKey] = id
	if len(f.createErrors) > 0 {
		err := f.createErrors[0]
		f.createErrors = f.createErrors[1:]
		return "", err
	}
	return id, nil
}

func (f *statefulPeeringAPI) AcceptVPCPeeringConnection(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed, ok := f.items[id]
	if !ok {
		return awserr.NotFound("peering not found")
	}
	if observed.Status != "pending-acceptance" {
		return fmt.Errorf("not pending")
	}
	observed.Status = "active"
	f.items[id] = observed
	f.updates++
	return nil
}

func (f *statefulPeeringAPI) DescribeVPCPeeringConnection(_ context.Context, id string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	observed, ok := f.items[id]
	if !ok {
		return ObservedState{}, awserr.NotFound("peering not found")
	}
	observed.Tags = maps.Clone(observed.Tags)
	observed.RequesterOptions = cloneOptions(observed.RequesterOptions)
	observed.AccepterOptions = cloneOptions(observed.AccepterOptions)
	return observed, nil
}

func (f *statefulPeeringAPI) DeleteVPCPeeringConnection(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed, ok := f.items[id]
	if !ok {
		return awserr.NotFound("peering not found")
	}
	delete(f.items, id)
	delete(f.owners, observed.Tags[managedKeyTag])
	f.deletes++
	return nil
}

func (f *statefulPeeringAPI) ModifyPeeringOptions(_ context.Context, id string, requester, accepter *PeeringOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed, ok := f.items[id]
	if !ok {
		return awserr.NotFound("peering not found")
	}
	observed.RequesterOptions = cloneOptions(requester)
	observed.AccepterOptions = cloneOptions(accepter)
	f.items[id] = observed
	f.updates++
	return nil
}

func (f *statefulPeeringAPI) UpdateTags(_ context.Context, id string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed, ok := f.items[id]
	if !ok {
		return awserr.NotFound("peering not found")
	}
	owner := observed.Tags[managedKeyTag]
	observed.Tags = maps.Clone(tags)
	observed.Tags[managedKeyTag] = owner
	f.items[id] = observed
	f.updates++
	return nil
}

func (f *statefulPeeringAPI) FindByManagedKey(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.owners[key], nil
}

func (f *statefulPeeringAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulPeeringAPI) seed(observed ObservedState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed.Tags = maps.Clone(observed.Tags)
	f.items[observed.VpcPeeringConnectionId] = observed
	if owner := observed.Tags[managedKeyTag]; owner != "" {
		f.owners[owner] = observed.VpcPeeringConnectionId
	}
}

func (f *statefulPeeringAPI) setStatus(id, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed := f.items[id]
	observed.Status = status
	f.items[id] = observed
}

func (f *statefulPeeringAPI) remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, id)
}

func setupGenericPeering(t *testing.T, api VPCPeeringAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericVPCPeeringDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) VPCPeeringAPI { return api })
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(peeringDriftSink{})).Ingress()
}

func managedPeeringSpec(autoAccept bool) VPCPeeringSpec {
	return VPCPeeringSpec{
		Account: "test", Region: "us-east-1", RequesterVpcId: "vpc-requester", AccepterVpcId: "vpc-accepter",
		AutoAccept: autoAccept, Tags: map[string]string{"env": "test"},
	}
}

func TestGenericVPCPeeringCoreLifecycle(t *testing.T) {
	api := newStatefulPeeringAPI()
	client := setupGenericPeering(t, api)
	key := "us-east-1~generic-peer"
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[VPCPeeringSpec, VPCPeeringOutputs]{
		Client: client, ServiceName: ServiceName, Key: key, Spec: managedPeeringSpec(true), Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs VPCPeeringSpec) { assert.Equal(t, key, inputs.ManagedKey) },
	})
}

func TestGenericVPCPeeringObservedImportLifecycle(t *testing.T) {
	api := newStatefulPeeringAPI()
	api.seed(ObservedState{VpcPeeringConnectionId: "pcx-existing", RequesterVpcId: "vpc-a", AccepterVpcId: "vpc-b", Status: "active", Tags: map[string]string{}})
	client := setupGenericPeering(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[VPCPeeringOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~existing-peer",
		Ref: types.ImportRef{Account: "test", ResourceID: "pcx-existing"}, Snapshot: api.snapshot,
	})
}

func TestGenericVPCPeeringAutoAcceptConvergesWhilePending(t *testing.T) {
	api := newStatefulPeeringAPI()
	client := setupGenericPeering(t, api)
	outputs, err := ingress.Object[VPCPeeringSpec, VPCPeeringOutputs](client, ServiceName, "us-east-1~auto-peer", "Provision").Request(t.Context(), managedPeeringSpec(true))
	require.NoError(t, err)
	assert.Equal(t, "active", outputs.Status)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.Equal(t, 1, api.snapshot().Updates)
}

func TestGenericVPCPeeringManualAcceptanceStaysPending(t *testing.T) {
	api := newStatefulPeeringAPI()
	client := setupGenericPeering(t, api)
	key := "us-east-1~manual-peer"
	outputs, err := ingress.Object[VPCPeeringSpec, VPCPeeringOutputs](client, ServiceName, key, "Provision").Request(t.Context(), managedPeeringSpec(false))
	require.NoError(t, err)
	assert.Equal(t, "pending-acceptance", outputs.Status)
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusPending, status.Status)
	api.setStatus(outputs.VpcPeeringConnectionId, "active")
	_, err = ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
}

func TestGenericVPCPeeringTerminalStateBecomesError(t *testing.T) {
	api := newStatefulPeeringAPI()
	api.createStatus = "rejected"
	client := setupGenericPeering(t, api)
	_, err := ingress.Object[VPCPeeringSpec, VPCPeeringOutputs](client, ServiceName, "us-east-1~rejected-peer", "Provision").Request(t.Context(), managedPeeringSpec(false))
	require.Error(t, err)
	status, statusErr := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, "us-east-1~rejected-peer", "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, statusErr)
	assert.Equal(t, types.StatusError, status.Status)
}

func TestGenericVPCPeeringRecoversAmbiguousCreate(t *testing.T) {
	api := newStatefulPeeringAPI()
	api.createErrors = []error{errors.New("create response lost")}
	client := setupGenericPeering(t, api)
	outputs, err := ingress.Object[VPCPeeringSpec, VPCPeeringOutputs](client, ServiceName, "us-east-1~ambiguous-peer", "Provision").Request(t.Context(), managedPeeringSpec(true))
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.VpcPeeringConnectionId)
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericVPCPeeringRejectsImmutableVPC(t *testing.T) {
	api := newStatefulPeeringAPI()
	client := setupGenericPeering(t, api)
	key := "us-east-1~immutable-peer"
	spec := managedPeeringSpec(true)
	_, err := ingress.Object[VPCPeeringSpec, VPCPeeringOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	spec.RequesterVpcId = "vpc-other"
	_, err = ingress.Object[VPCPeeringSpec, VPCPeeringOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requesterVpcId is immutable")
	stored, storedErr := ingress.Object[restate.Void, VPCPeeringSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, storedErr)
	assert.Equal(t, "vpc-requester", stored.RequesterVpcId)
}

func TestGenericVPCPeeringProvisionChangeRejectsEveryImmutableField(t *testing.T) {
	previous := managedPeeringSpec(true)
	cases := map[string]func(*VPCPeeringSpec){
		"account":     func(spec *VPCPeeringSpec) { spec.Account = "other" },
		"region":      func(spec *VPCPeeringSpec) { spec.Region = "us-west-2" },
		"requester":   func(spec *VPCPeeringSpec) { spec.RequesterVpcId = "vpc-other" },
		"accepter":    func(spec *VPCPeeringSpec) { spec.AccepterVpcId = "vpc-other" },
		"peer-owner":  func(spec *VPCPeeringSpec) { spec.PeerOwnerId = "999999999999" },
		"peer-region": func(spec *VPCPeeringSpec) { spec.PeerRegion = "us-west-2" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			next := previous
			mutate(&next)
			err := (&genericOperations{}).ConvergeProvisionChange(nil, previous, next, ObservedState{})
			require.Error(t, err)
			assert.EqualValues(t, 409, restate.ErrorCode(err))
		})
	}
}

func TestGenericVPCPeeringExternalDeleteRequiresExplicitProvision(t *testing.T) {
	api := newStatefulPeeringAPI()
	client := setupGenericPeering(t, api)
	key := "us-east-1~external-peer"
	outputs, err := ingress.Object[VPCPeeringSpec, VPCPeeringOutputs](client, ServiceName, key, "Provision").Request(t.Context(), managedPeeringSpec(true))
	require.NoError(t, err)
	before := api.snapshot()
	api.remove(outputs.VpcPeeringConnectionId)
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Equal(t, before.Creates, api.snapshot().Creates)
}
