package routetable

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/pkg/types"
)

type fakeRouteTableAPI struct {
	mu                sync.Mutex
	nextID            string
	createCalls       int
	deleteCalls       int
	updateCalls       int
	createRouteCalls  []Route
	replaceRouteCalls []Route
	deleteRouteCalls  []string
	associateCalls    []string
	disassociateCalls []string
	observed          map[string]ObservedState
	managedKeys       map[string]string
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
	tags := map[string]string{"praxis:managed-key": spec.ManagedKey}
	for key, value := range spec.Tags {
		tags[key] = value
	}
	f.observed[id] = ObservedState{RouteTableId: id, VpcId: spec.VpcId, Tags: tags}
	f.managedKeys[spec.ManagedKey] = id
	return id, nil
}

func (f *fakeRouteTableAPI) DescribeRouteTable(ctx context.Context, routeTableId string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obs, ok := f.observed[routeTableId]
	if !ok {
		return ObservedState{}, &mockAPIError{code: "InvalidRouteTableID.NotFound", message: "missing"}
	}
	cloned := obs
	cloned.Routes = append([]ObservedRoute(nil), obs.Routes...)
	cloned.Associations = append([]ObservedAssociation(nil), obs.Associations...)
	cloned.Tags = map[string]string{}
	for key, value := range obs.Tags {
		cloned.Tags[key] = value
	}
	return cloned, nil
}

func (f *fakeRouteTableAPI) DeleteRouteTable(ctx context.Context, routeTableId string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
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
	for _, route := range obs.Routes {
		if route.DestinationCidrBlock != destinationCidr {
			filtered = append(filtered, route)
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
	for i, existing := range obs.Routes {
		if existing.DestinationCidrBlock == route.DestinationCidrBlock {
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
	for key, value := range tags {
		merged[key] = value
	}
	obs.Tags = merged
	f.observed[routeTableId] = obs
	return nil
}

func (f *fakeRouteTableAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.managedKeys[managedKey], nil
}

func setupRouteTableDriver(t *testing.T, api *fakeRouteTableAPI) *ingress.Client {
	t.Helper()
	driver := NewRouteTableDriverWithFactory(nil, func(cfg aws.Config) RouteTableAPI {
		return api
	})
	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress()
}

func TestProvision_CreatesNewRouteTable(t *testing.T) {
	api := newFakeRouteTableAPI()
	client := setupRouteTableDriver(t, api)
	key := "vpc-123~public-rt"

	outputs, err := ingress.Object[RouteTableSpec, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), RouteTableSpec{
		Region:       "us-east-1",
		VpcId:        "vpc-123",
		Routes:       []Route{{DestinationCidrBlock: "0.0.0.0/0", GatewayId: "igw-123"}},
		Associations: []Association{{SubnetId: "subnet-123"}},
		Tags:         map[string]string{"Name": "public-rt"},
		ManagedKey:   key,
	})
	require.NoError(t, err)
	assert.Equal(t, "rtb-123", outputs.RouteTableId)
	assert.Equal(t, []string{"subnet-123"}, api.associateCalls)
	require.Len(t, api.createRouteCalls, 1)
	assert.Equal(t, "0.0.0.0/0", api.createRouteCalls[0].DestinationCidrBlock)
}

func TestProvision_RouteWithMultipleTargetsFails(t *testing.T) {
	api := newFakeRouteTableAPI()
	client := setupRouteTableDriver(t, api)
	_, err := ingress.Object[RouteTableSpec, RouteTableOutputs](client, ServiceName, "vpc-123~public-rt", "Provision").Request(t.Context(), RouteTableSpec{
		Region: "us-east-1",
		VpcId:  "vpc-123",
		Routes: []Route{{DestinationCidrBlock: "0.0.0.0/0", GatewayId: "igw-123", NatGatewayId: "nat-123"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one target")
}

func TestProvision_ReplaceRouteTarget(t *testing.T) {
	api := newFakeRouteTableAPI()
	client := setupRouteTableDriver(t, api)
	key := "vpc-123~public-rt"

	_, err := ingress.Object[RouteTableSpec, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), RouteTableSpec{
		Region:     "us-east-1",
		VpcId:      "vpc-123",
		ManagedKey: key,
		Routes:     []Route{{DestinationCidrBlock: "0.0.0.0/0", GatewayId: "igw-123"}},
		Tags:       map[string]string{"Name": "public-rt"},
	})
	require.NoError(t, err)

	_, err = ingress.Object[RouteTableSpec, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), RouteTableSpec{
		Region:     "us-east-1",
		VpcId:      "vpc-123",
		ManagedKey: key,
		Routes:     []Route{{DestinationCidrBlock: "0.0.0.0/0", NatGatewayId: "nat-123"}},
		Tags:       map[string]string{"Name": "public-rt"},
	})
	require.NoError(t, err)
	require.Len(t, api.replaceRouteCalls, 1)
	assert.Equal(t, "nat-123", api.replaceRouteCalls[0].NatGatewayId)
}

func TestDelete_MainRouteTableBlocked(t *testing.T) {
	api := newFakeRouteTableAPI()
	client := setupRouteTableDriver(t, api)
	key := "vpc-123~public-rt"
	outputs, err := ingress.Object[RouteTableSpec, RouteTableOutputs](client, ServiceName, key, "Provision").Request(t.Context(), RouteTableSpec{
		Region:     "us-east-1",
		VpcId:      "vpc-123",
		ManagedKey: key,
	})
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
