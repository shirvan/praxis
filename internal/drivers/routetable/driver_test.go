package routetable

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"

	"github.com/restatedev/sdk-go/ingress"
	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	"github.com/shirvan/praxis/internal/eventing"
	restatetest "github.com/shirvan/praxis/internal/restatetest"

	"github.com/shirvan/praxis/pkg/types"
)

const routeTableDriftRecorderObjectServiceName = "RouteTableTestDriftRecorder"

type routeTableDriftRecorder struct{}

func (routeTableDriftRecorder) ServiceName() string {
	return eventing.ResourceEventBridgeServiceName
}

func (routeTableDriftRecorder) ReportDrift(ctx restate.Context, req eventing.DriftReportRequest) error {
	_, err := restate.WithRequestType[eventing.DriftReportRequest, restate.Void](
		restate.Object[restate.Void](ctx, routeTableDriftRecorderObjectServiceName, req.ResourceKey, "Append"),
	).Request(req)
	return err
}

type routeTableDriftRecorderObject struct{}

func (routeTableDriftRecorderObject) ServiceName() string {
	return routeTableDriftRecorderObjectServiceName
}

func (routeTableDriftRecorderObject) Append(ctx restate.ObjectContext, req eventing.DriftReportRequest) error {
	reports, err := restate.Get[[]eventing.DriftReportRequest](ctx, "reports")
	if err != nil {
		return err
	}
	reports = append(reports, req)
	restate.Set(ctx, "reports", reports)
	return nil
}

func (routeTableDriftRecorderObject) List(ctx restate.ObjectSharedContext, _ restate.Void) ([]eventing.DriftReportRequest, error) {
	reports, err := restate.Get[[]eventing.DriftReportRequest](ctx, "reports")
	if err != nil || reports == nil {
		return nil, err
	}
	return reports, nil
}

type fakeRouteTableAPI struct {
	mu                sync.Mutex
	nextID            string
	createCalls       int
	describeCalls     int
	deleteCalls       int
	updateCalls       int
	createRouteCalls  []Route
	replaceRouteCalls []Route
	deleteRouteCalls  []string
	associateCalls    []string
	disassociateCalls []string
	observed          map[string]ObservedState
	managedKeys       map[string]string
	deleteError       error
	findError         error
	createErrors      []error
}

func newFakeRouteTableAPI() *fakeRouteTableAPI {
	return &fakeRouteTableAPI{
		nextID:      "rtb-123",
		observed:    map[string]ObservedState{},
		managedKeys: map[string]string{},
	}
}

func (f *fakeRouteTableAPI) CreateRouteTable(ctx context.Context, spec RouteTableSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	id := f.nextID
	tags := map[string]string{routeTableManagedKeyTag: spec.ManagedKey}
	maps.Copy(tags, drivers.FilterPraxisTags(spec.Tags))
	f.observed[id] = ObservedState{RouteTableId: id, VpcId: spec.VpcId, Tags: tags}
	f.managedKeys[spec.ManagedKey] = id
	if len(f.createErrors) > 0 {
		err := f.createErrors[0]
		f.createErrors = f.createErrors[1:]
		return "", err
	}
	return id, nil
}

func (f *fakeRouteTableAPI) DescribeRouteTable(ctx context.Context, routeTableId string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.describeCalls++
	obs, ok := f.observed[routeTableId]
	if !ok {
		return ObservedState{}, &mockAPIError{code: "InvalidRouteTableID.NotFound", message: "missing"}
	}
	cloned := obs
	cloned.Routes = append([]ObservedRoute(nil), obs.Routes...)
	cloned.Associations = append([]ObservedAssociation(nil), obs.Associations...)
	cloned.Tags = map[string]string{}
	maps.Copy(cloned.Tags, obs.Tags)
	return cloned, nil
}

func (f *fakeRouteTableAPI) DeleteRouteTable(ctx context.Context, routeTableId string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	if f.deleteError != nil {
		return f.deleteError
	}
	delete(f.observed, routeTableId)
	return nil
}

func (f *fakeRouteTableAPI) CreateRoute(ctx context.Context, routeTableId string, route Route) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createRouteCalls = append(f.createRouteCalls, route)
	obs := f.observed[routeTableId]
	obs.Routes = append(obs.Routes, ObservedRoute{DestinationCidrBlock: route.DestinationCidrBlock, GatewayId: route.GatewayId, NatGatewayId: route.NatGatewayId, VpcPeeringConnectionId: route.VpcPeeringConnectionId, TransitGatewayId: route.TransitGatewayId, NetworkInterfaceId: route.NetworkInterfaceId, VpcEndpointId: route.VpcEndpointId, Origin: "CreateRoute", State: "active"})
	f.observed[routeTableId] = obs
	return nil
}

func (f *fakeRouteTableAPI) DeleteRoute(ctx context.Context, routeTableId string, destinationCidr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteRouteCalls = append(f.deleteRouteCalls, destinationCidr)
	obs := f.observed[routeTableId]
	filtered := make([]ObservedRoute, 0, len(obs.Routes))
	for i := range obs.Routes {
		if obs.Routes[i].DestinationCidrBlock != destinationCidr {
			filtered = append(filtered, obs.Routes[i])
		}
	}
	obs.Routes = filtered
	f.observed[routeTableId] = obs
	return nil
}

func (f *fakeRouteTableAPI) ReplaceRoute(ctx context.Context, routeTableId string, route Route) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replaceRouteCalls = append(f.replaceRouteCalls, route)
	obs := f.observed[routeTableId]
	for i := range obs.Routes {
		if obs.Routes[i].DestinationCidrBlock == route.DestinationCidrBlock {
			obs.Routes[i] = ObservedRoute{DestinationCidrBlock: route.DestinationCidrBlock, GatewayId: route.GatewayId, NatGatewayId: route.NatGatewayId, VpcPeeringConnectionId: route.VpcPeeringConnectionId, TransitGatewayId: route.TransitGatewayId, NetworkInterfaceId: route.NetworkInterfaceId, VpcEndpointId: route.VpcEndpointId, Origin: "CreateRoute", State: "active"}
			f.observed[routeTableId] = obs
			return nil
		}
	}
	return &mockAPIError{code: "InvalidRoute.NotFound", message: "missing"}
}

func (f *fakeRouteTableAPI) AssociateSubnet(ctx context.Context, routeTableId string, subnetId string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.associateCalls = append(f.associateCalls, subnetId)
	obs := f.observed[routeTableId]
	associationID := fmt.Sprintf("rtbassoc-%d", len(obs.Associations)+1)
	obs.Associations = append(obs.Associations, ObservedAssociation{AssociationId: associationID, SubnetId: subnetId})
	f.observed[routeTableId] = obs
	return associationID, nil
}

func (f *fakeRouteTableAPI) DisassociateSubnet(ctx context.Context, associationId string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disassociateCalls = append(f.disassociateCalls, associationId)
	for routeTableID, obs := range f.observed {
		filtered := make([]ObservedAssociation, 0, len(obs.Associations))
		for _, association := range obs.Associations {
			if association.AssociationId != associationId {
				filtered = append(filtered, association)
			}
		}
		obs.Associations = filtered
		f.observed[routeTableID] = obs
	}
	return nil
}

func (f *fakeRouteTableAPI) UpdateTags(ctx context.Context, routeTableId string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	obs := f.observed[routeTableId]
	merged := map[string]string{"praxis:managed-key": obs.Tags["praxis:managed-key"]}
	maps.Copy(merged, tags)
	obs.Tags = merged
	f.observed[routeTableId] = obs
	return nil
}

func (f *fakeRouteTableAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.findError != nil {
		return "", f.findError
	}
	return f.managedKeys[managedKey], nil
}

func (f *fakeRouteTableAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{
		Creates: f.createCalls,
		Reads:   f.describeCalls,
		Updates: f.updateCalls + len(f.createRouteCalls) + len(f.replaceRouteCalls) + len(f.deleteRouteCalls) + len(f.associateCalls) + len(f.disassociateCalls),
		Deletes: f.deleteCalls,
	}
}

func setupRouteTableDriver(t *testing.T, api *fakeRouteTableAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericRouteTableDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(cfg aws.Config) RouteTableAPI {
		return api
	})
	env := restatetest.Start(t,
		restate.Reflect(driver),
		restate.Reflect(routeTableDriftRecorder{}),
		restate.Reflect(routeTableDriftRecorderObject{}),
	)
	return env.Ingress()
}

func TestGenericRouteTableCoreLifecycle(t *testing.T) {
	api := newFakeRouteTableAPI()
	client := setupRouteTableDriver(t, api)
	key := "vpc-123~core-route-table"
	spec := RouteTableSpec{
		Account: "test", Region: "us-east-1", VpcId: "vpc-123",
		Routes:       []Route{{DestinationCidrBlock: "0.0.0.0/0", GatewayId: "igw-123"}},
		Associations: []Association{{SubnetId: "subnet-123"}}, Tags: map[string]string{"Name": "core"},
	}
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[RouteTableSpec, RouteTableOutputs]{
		Client: client, ServiceName: ServiceName, Key: key, Spec: spec, Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs RouteTableSpec) {
			assert.Equal(t, key, inputs.ManagedKey)
			assert.Equal(t, "us-east-1", inputs.Region)
			assert.Equal(t, spec.Routes, inputs.Routes)
		},
	})
}

func TestGenericRouteTableObservedImportLifecycle(t *testing.T) {
	api := newFakeRouteTableAPI()
	api.observed["rtb-import"] = ObservedState{
		RouteTableId: "rtb-import", VpcId: "vpc-import", OwnerId: "123456789012",
		Routes: []ObservedRoute{{DestinationCidrBlock: "10.0.0.0/16", GatewayId: "local", Origin: "CreateRouteTable", State: "active"}},
		Tags:   map[string]string{"Name": "imported"},
	}
	client := setupRouteTableDriver(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[RouteTableOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~rtb-import",
		Ref: types.ImportRef{ResourceID: "rtb-import", Account: "test"}, Snapshot: api.snapshot,
	})
}

func pollRouteTableEventTypes(t *testing.T, client *ingress.Client, resourceKey string, expected ...string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for {
		records, err := ingress.Object[restate.Void, []eventing.DriftReportRequest](client, routeTableDriftRecorderObjectServiceName, resourceKey, "List").Request(t.Context(), restate.Void{})
		if err != nil {
			lastErr = err
			if time.Now().After(deadline) {
				require.NoError(t, err)
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		typesSeen := make([]string, 0, len(records))
		seen := make(map[string]bool, len(records))
		for _, record := range records {
			typesSeen = append(typesSeen, record.EventType)
			seen[record.EventType] = true
		}
		complete := true
		for _, want := range expected {
			if !seen[want] {
				complete = false
				break
			}
		}
		if complete || time.Now().After(deadline) {
			if !complete && lastErr != nil {
				require.NoError(t, lastErr)
			}
			return typesSeen
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestProvision_CreatesNewRouteTable(t *testing.T) {
	api := newFakeRouteTableAPI()
	client := setupRouteTableDriver(t, api)
	key := "vpc-123~public-rt"

	outputs, err := ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, RouteTableSpec{
		Region:       "us-east-1",
		VpcId:        "vpc-123",
		Routes:       []Route{{DestinationCidrBlock: "0.0.0.0/0", GatewayId: "igw-123"}},
		Associations: []Association{{SubnetId: "subnet-123"}},
		Tags:         map[string]string{"Name": "public-rt"},
		ManagedKey:   key,
	}))
	require.NoError(t, err)
	assert.Equal(t, "rtb-123", outputs.RouteTableId)
	assert.Equal(t, []string{"subnet-123"}, api.associateCalls)
	require.Len(t, api.createRouteCalls, 1)
	assert.Equal(t, "0.0.0.0/0", api.createRouteCalls[0].DestinationCidrBlock)
}

func TestProvision_RouteWithMultipleTargetsFails(t *testing.T) {
	api := newFakeRouteTableAPI()
	client := setupRouteTableDriver(t, api)
	_, err := ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, "vpc-123~public-rt", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, RouteTableSpec{
		Region: "us-east-1",
		VpcId:  "vpc-123",
		Routes: []Route{{DestinationCidrBlock: "0.0.0.0/0", GatewayId: "igw-123", NatGatewayId: "nat-123"}},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one target")
	assert.Contains(t, err.Error(), "400")
	assert.Zero(t, api.snapshot().Creates)
}

func TestProvision_ReplaceRouteTarget(t *testing.T) {
	api := newFakeRouteTableAPI()
	client := setupRouteTableDriver(t, api)
	key := "vpc-123~public-rt"

	_, err := ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, RouteTableSpec{
		Region:     "us-east-1",
		VpcId:      "vpc-123",
		ManagedKey: key,
		Routes:     []Route{{DestinationCidrBlock: "0.0.0.0/0", GatewayId: "igw-123"}},
		Tags:       map[string]string{"Name": "public-rt"},
	}))
	require.NoError(t, err)

	_, err = ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, RouteTableSpec{
		Region:     "us-east-1",
		VpcId:      "vpc-123",
		ManagedKey: key,
		Routes:     []Route{{DestinationCidrBlock: "0.0.0.0/0", NatGatewayId: "nat-123"}},
		Tags:       map[string]string{"Name": "public-rt"},
	}))
	require.NoError(t, err)
	require.Len(t, api.replaceRouteCalls, 1)
	assert.Equal(t, "nat-123", api.replaceRouteCalls[0].NatGatewayId)
}

func TestGenericRouteTableAdoptsExactManagedKeyAndConverges(t *testing.T) {
	api := newFakeRouteTableAPI()
	key := "vpc-123~adopted"
	api.observed["rtb-existing"] = ObservedState{
		RouteTableId: "rtb-existing", VpcId: "vpc-123",
		Tags: map[string]string{routeTableManagedKeyTag: key, "env": "stale"},
	}
	api.managedKeys[key] = "rtb-existing"
	client := setupRouteTableDriver(t, api)

	outputs, err := ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, RouteTableSpec{
		Account: "test", Region: "us-east-1", VpcId: "vpc-123",
		Routes: []Route{{DestinationCidrBlock: "0.0.0.0/0", GatewayId: "igw-123"}},
		Tags:   map[string]string{"env": "prod"},
	}))
	require.NoError(t, err)
	assert.Equal(t, "rtb-existing", outputs.RouteTableId)
	assert.Zero(t, api.snapshot().Creates)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Equal(t, "prod", api.observed["rtb-existing"].Tags["env"])
	assert.Equal(t, key, api.observed["rtb-existing"].Tags[routeTableManagedKeyTag])
	assert.Len(t, api.observed["rtb-existing"].Routes, 1)
}

func TestGenericRouteTableRejectsImmutableVPCChange(t *testing.T) {
	api := newFakeRouteTableAPI()
	key := "vpc-old~immutable"
	api.observed["rtb-existing"] = ObservedState{
		RouteTableId: "rtb-existing", VpcId: "vpc-old", Tags: map[string]string{routeTableManagedKeyTag: key},
	}
	api.managedKeys[key] = "rtb-existing"
	client := setupRouteTableDriver(t, api)

	_, err := ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, RouteTableSpec{
		Account: "test", Region: "us-east-1", VpcId: "vpc-new",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vpcId is immutable")
	assert.Contains(t, err.Error(), "409")
	assert.Zero(t, api.snapshot().Creates)
	assert.Zero(t, api.snapshot().Updates)
}

func TestGenericRouteTableRejectsDifferentManagedOwner(t *testing.T) {
	api := newFakeRouteTableAPI()
	client := setupRouteTableDriver(t, api)
	key := "vpc-123~ownership"
	spec := RouteTableSpec{Account: "test", Region: "us-east-1", VpcId: "vpc-123"}
	outputs, err := ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	api.mu.Lock()
	observed := api.observed[outputs.RouteTableId]
	observed.Tags[routeTableManagedKeyTag] = "vpc-123~different-owner"
	api.observed[outputs.RouteTableId] = observed
	api.mu.Unlock()
	before := api.snapshot()

	_, err = ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "different-owner")
	assert.Contains(t, err.Error(), "409")
	after := api.snapshot()
	assert.Equal(t, before.Creates, after.Creates)
	assert.Equal(t, before.Updates, after.Updates)

	_, deleteErr := ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, deleteErr)
	assert.Contains(t, deleteErr.Error(), "refusing to delete")
	assert.Contains(t, deleteErr.Error(), "different-owner")
	assert.Equal(t, before.Deletes, api.snapshot().Deletes)
}

func TestGenericRouteTableAmbiguousManagedOwnershipIsTerminal(t *testing.T) {
	api := newFakeRouteTableAPI()
	api.findError = errors.New("ownership corruption: 2 route tables claim managed-key")
	client := setupRouteTableDriver(t, api)

	_, err := ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, "vpc-123~ambiguous", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, RouteTableSpec{
		Account: "test", Region: "us-east-1", VpcId: "vpc-123",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ownership corruption")
	assert.Contains(t, err.Error(), "409")
	assert.Zero(t, api.snapshot().Creates)
}

func TestGenericRouteTableAmbiguousCreateResponseAdoptsWithoutDuplicate(t *testing.T) {
	api := newFakeRouteTableAPI()
	api.createErrors = []error{errors.New("ServiceUnavailable: response lost after CreateRouteTable")}
	client := setupRouteTableDriver(t, api)
	key := "vpc-123~ambiguous-create"

	outputs, err := ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, RouteTableSpec{
		Account: "test", Region: "us-east-1", VpcId: "vpc-123", Tags: map[string]string{"env": "prod"},
	}))
	require.NoError(t, err)
	assert.Equal(t, "rtb-123", outputs.RouteTableId)
	assert.Equal(t, 1, api.snapshot().Creates, "the retry must adopt the atomically tagged table instead of creating another")
}

func TestGenericRouteTableCallerCannotOverrideManagedKeyTag(t *testing.T) {
	api := newFakeRouteTableAPI()
	client := setupRouteTableDriver(t, api)
	key := "vpc-123~authoritative-key"

	outputs, err := ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, RouteTableSpec{
		Account: "test", Region: "us-east-1", VpcId: "vpc-123",
		Tags: map[string]string{routeTableManagedKeyTag: "spoofed", "env": "prod"},
	}))
	require.NoError(t, err)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Equal(t, key, api.observed[outputs.RouteTableId].Tags[routeTableManagedKeyTag])
	assert.Equal(t, "prod", api.observed[outputs.RouteTableId].Tags["env"])
}

func TestDelete_MainRouteTableBlocked(t *testing.T) {
	api := newFakeRouteTableAPI()
	client := setupRouteTableDriver(t, api)
	key := "vpc-123~public-rt"
	outputs, err := ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, RouteTableSpec{
		Region:     "us-east-1",
		VpcId:      "vpc-123",
		ManagedKey: key,
	}))
	require.NoError(t, err)
	api.mu.Lock()
	obs := api.observed[outputs.RouteTableId]
	obs.Associations = []ObservedAssociation{{AssociationId: "rtbassoc-main", Main: true}}
	api.observed[outputs.RouteTableId] = obs
	api.mu.Unlock()

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "main route table")
}

func TestGenericRouteTableDeleteCleansOwnedRoutesAndAssociations(t *testing.T) {
	api := newFakeRouteTableAPI()
	client := setupRouteTableDriver(t, api)
	key := "vpc-123~delete-cleanup"
	_, err := ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, RouteTableSpec{
		Account: "test", Region: "us-east-1", VpcId: "vpc-123",
		Routes:       []Route{{DestinationCidrBlock: "0.0.0.0/0", GatewayId: "igw-123"}},
		Associations: []Association{{SubnetId: "subnet-123"}},
	}))
	require.NoError(t, err)
	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Equal(t, []string{"0.0.0.0/0"}, api.deleteRouteCalls)
	assert.Len(t, api.disassociateCalls, 1)
	assert.Equal(t, 1, api.deleteCalls)
}

func TestGenericRouteTableDeleteDependencyConflictPreservesProviderTable(t *testing.T) {
	api := newFakeRouteTableAPI()
	client := setupRouteTableDriver(t, api)
	key := "vpc-123~dependency-conflict"
	outputs, err := ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, RouteTableSpec{
		Account: "test", Region: "us-east-1", VpcId: "vpc-123",
	}))
	require.NoError(t, err)
	api.mu.Lock()
	api.deleteError = &mockAPIError{code: "DependencyViolation", message: "resource is still referenced"}
	api.mu.Unlock()
	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DependencyViolation")
	api.mu.Lock()
	defer api.mu.Unlock()
	_, exists := api.observed[outputs.RouteTableId]
	assert.True(t, exists)
}

func TestImport_DefaultsToObservedMode(t *testing.T) {
	api := newFakeRouteTableAPI()
	api.observed["rtb-123"] = ObservedState{RouteTableId: "rtb-123", VpcId: "vpc-123"}
	client := setupRouteTableDriver(t, api)
	key := "us-east-1~rtb-123"

	_, err := ingress.Object[types.ImportRef, RouteTableOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "rtb-123"})
	require.NoError(t, err)
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestReconcile_RouteDriftCorrected_EmitsEvents(t *testing.T) {
	api := newFakeRouteTableAPI()
	client := setupRouteTableDriver(t, api)
	key := "vpc-123~public-rt"

	_, err := ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, RouteTableSpec{
		Account:    "test",
		Region:     "us-east-1",
		VpcId:      "vpc-123",
		ManagedKey: key,
		Routes:     []Route{{DestinationCidrBlock: "0.0.0.0/0", GatewayId: "igw-123"}},
		Tags:       map[string]string{"Name": "public-rt"},
	}))
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.observed["rtb-123"]
	obs.Tags["Name"] = "stale-rt"
	api.observed["rtb-123"] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Equal(t, "public-rt", api.observed["rtb-123"].Tags["Name"])
	assert.Equal(t, []string{eventing.DriftEventDetected, eventing.DriftEventCorrected}, pollRouteTableEventTypes(t, client, key, eventing.DriftEventDetected, eventing.DriftEventCorrected))
}

func TestReconcile_ObservedModeReportsOnly_EmitsDetected(t *testing.T) {
	api := newFakeRouteTableAPI()
	api.observed["rtb-123"] = ObservedState{RouteTableId: "rtb-123", VpcId: "vpc-123", Tags: map[string]string{"env": "dev"}}
	client := setupRouteTableDriver(t, api)
	key := "us-east-1~rtb-123"

	_, err := ingress.Object[types.ImportRef, RouteTableOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "rtb-123", Account: "test"})
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.observed["rtb-123"]
	obs.Tags["env"] = "prod"
	api.observed["rtb-123"] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.False(t, result.Correcting)
	assert.Equal(t, []string{eventing.DriftEventDetected}, pollRouteTableEventTypes(t, client, key, eventing.DriftEventDetected))
}

func TestReconcile_ExternalDelete_EmitsEvent(t *testing.T) {
	api := newFakeRouteTableAPI()
	client := setupRouteTableDriver(t, api)
	key := "vpc-123~public-rt"

	_, err := ingress.Object[types.ProvisionRequest, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, RouteTableSpec{
		Account:    "test",
		Region:     "us-east-1",
		VpcId:      "vpc-123",
		ManagedKey: key,
		Tags:       map[string]string{"Name": "public-rt"},
	}))
	require.NoError(t, err)

	api.mu.Lock()
	delete(api.observed, "rtb-123")
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "deleted externally")
	assert.True(t, result.ReplacementRequired)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.Equal(t, []string{eventing.DriftEventExternalDelete}, pollRouteTableEventTypes(t, client, key, eventing.DriftEventExternalDelete))
}
