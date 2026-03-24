package subnet

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
	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/pkg/types"
)

type fakeSubnetAPI struct {
	mu sync.Mutex

	nextID      string
	observed    map[string]ObservedState
	managedKeys map[string]string

	createCalls int
	waitCalls   int
	modifyCalls int
	updateCalls int
	deleteCalls int

	modifyValues []bool

	createFunc   func(context.Context, SubnetSpec) (string, error)
	describeFunc func(context.Context, string) (ObservedState, error)
	deleteFunc   func(context.Context, string) error
	waitFunc     func(context.Context, string) error
	modifyFunc   func(context.Context, string, bool) error
	updateFunc   func(context.Context, string, map[string]string) error
	findFunc     func(context.Context, string) (string, error)
}

func newFakeSubnetAPI() *fakeSubnetAPI {
	return &fakeSubnetAPI{
		nextID:      "subnet-123",
		observed:    map[string]ObservedState{},
		managedKeys: map[string]string{},
	}
}

func (f *fakeSubnetAPI) CreateSubnet(ctx context.Context, spec SubnetSpec) (string, error) {
	if f.createFunc != nil {
		return f.createFunc(ctx, spec)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	id := f.nextID
	if id == "" {
		id = fmt.Sprintf("subnet-%d", f.createCalls)
	}
	tags := map[string]string{"praxis:managed-key": spec.ManagedKey}
	for key, value := range spec.Tags {
		tags[key] = value
	}
	f.observed[id] = ObservedState{
		SubnetId:            id,
		VpcId:               spec.VpcId,
		CidrBlock:           spec.CidrBlock,
		AvailabilityZone:    spec.AvailabilityZone,
		AvailabilityZoneId:  "use1-az1",
		MapPublicIpOnLaunch: false,
		State:               "available",
		OwnerId:             "123456789012",
		AvailableIpCount:    251,
		Tags:                tags,
	}
	if spec.ManagedKey != "" {
		f.managedKeys[spec.ManagedKey] = id
	}
	return id, nil
}

func (f *fakeSubnetAPI) DescribeSubnet(ctx context.Context, subnetID string) (ObservedState, error) {
	if f.describeFunc != nil {
		return f.describeFunc(ctx, subnetID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obs, ok := f.observed[subnetID]
	if !ok {
		return ObservedState{}, &mockAPIError{code: "InvalidSubnetID.NotFound", message: "missing"}
	}
	return cloneObserved(obs), nil
}

func (f *fakeSubnetAPI) DeleteSubnet(ctx context.Context, subnetID string) error {
	if f.deleteFunc != nil {
		return f.deleteFunc(ctx, subnetID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	if _, ok := f.observed[subnetID]; !ok {
		return &mockAPIError{code: "InvalidSubnetID.NotFound", message: "missing"}
	}
	delete(f.observed, subnetID)
	for key, value := range f.managedKeys {
		if value == subnetID {
			delete(f.managedKeys, key)
		}
	}
	return nil
}

func (f *fakeSubnetAPI) WaitUntilAvailable(ctx context.Context, subnetID string) error {
	if f.waitFunc != nil {
		return f.waitFunc(ctx, subnetID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waitCalls++
	obs := f.observed[subnetID]
	obs.State = "available"
	f.observed[subnetID] = obs
	return nil
}

func (f *fakeSubnetAPI) ModifyMapPublicIp(ctx context.Context, subnetID string, enabled bool) error {
	if f.modifyFunc != nil {
		return f.modifyFunc(ctx, subnetID, enabled)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.modifyCalls++
	f.modifyValues = append(f.modifyValues, enabled)
	obs, ok := f.observed[subnetID]
	if !ok {
		return &mockAPIError{code: "InvalidSubnetID.NotFound", message: "missing"}
	}
	obs.MapPublicIpOnLaunch = enabled
	f.observed[subnetID] = obs
	return nil
}

func (f *fakeSubnetAPI) UpdateTags(ctx context.Context, subnetID string, tags map[string]string) error {
	if f.updateFunc != nil {
		return f.updateFunc(ctx, subnetID, tags)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	obs, ok := f.observed[subnetID]
	if !ok {
		return &mockAPIError{code: "InvalidSubnetID.NotFound", message: "missing"}
	}
	praxisTags := map[string]string{}
	for key, value := range obs.Tags {
		if len(key) >= 7 && key[:7] == "praxis:" {
			praxisTags[key] = value
		}
	}
	obs.Tags = map[string]string{}
	for key, value := range praxisTags {
		obs.Tags[key] = value
	}
	for key, value := range tags {
		obs.Tags[key] = value
	}
	f.observed[subnetID] = obs
	return nil
}

func (f *fakeSubnetAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	if f.findFunc != nil {
		return f.findFunc(ctx, managedKey)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.managedKeys[managedKey], nil
}

func (f *fakeSubnetAPI) FindByTags(ctx context.Context, tags map[string]string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var matches []string
	for id, observed := range f.observed {
		matched := true
		for key, value := range tags {
			if observed.Tags[key] != value {
				matched = false
				break
			}
		}
		if matched {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous lookup")
	}
}

func cloneObserved(obs ObservedState) ObservedState {
	clone := obs
	if obs.Tags != nil {
		clone.Tags = make(map[string]string, len(obs.Tags))
		for key, value := range obs.Tags {
			clone.Tags[key] = value
		}
	}
	return clone
}

func setupSubnetDriver(t *testing.T, api SubnetAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")

	driver := NewSubnetDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(cfg aws.Config) SubnetAPI {
		return api
	})
	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress()
}

func testSubnetSpec(key, vpcID string, tags map[string]string) SubnetSpec {
	if tags == nil {
		tags = map[string]string{"Name": "public-a"}
	}
	return SubnetSpec{
		Account:          "test",
		Region:           "us-east-1",
		VpcId:            vpcID,
		CidrBlock:        "10.0.1.0/24",
		AvailabilityZone: "us-east-1a",
		Tags:             tags,
		ManagedKey:       key,
	}
}

func TestServiceName(t *testing.T) {
	drv := NewSubnetDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestProvision_CreatesNewSubnet(t *testing.T) {
	api := newFakeSubnetAPI()
	client := setupSubnetDriver(t, api)
	key := "vpc-123~public-a"

	outputs, err := ingress.Object[SubnetSpec, SubnetOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSubnetSpec(key, "vpc-123", map[string]string{"Name": "public-a", "env": "dev"}))
	require.NoError(t, err)
	assert.Equal(t, "subnet-123", outputs.SubnetId)
	assert.Equal(t, "vpc-123", outputs.VpcId)
	assert.Equal(t, "10.0.1.0/24", outputs.CidrBlock)
	assert.Equal(t, "us-east-1a", outputs.AvailabilityZone)
	assert.Equal(t, 1, api.createCalls)
	assert.Equal(t, 1, api.waitCalls)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)

	storedOutputs, err := ingress.Object[restate.Void, SubnetOutputs](client, ServiceName, key, "GetOutputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, outputs.SubnetId, storedOutputs.SubnetId)
}

func TestProvision_MissingCidrBlockFails(t *testing.T) {
	api := newFakeSubnetAPI()
	client := setupSubnetDriver(t, api)
	key := "vpc-123~public-a"
	spec := testSubnetSpec(key, "vpc-123", nil)
	spec.CidrBlock = ""

	_, err := ingress.Object[SubnetSpec, SubnetOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cidrBlock is required")
}

func TestProvision_MissingVpcIdFails(t *testing.T) {
	api := newFakeSubnetAPI()
	client := setupSubnetDriver(t, api)
	key := "vpc-123~public-a"
	spec := testSubnetSpec(key, "vpc-123", nil)
	spec.VpcId = ""

	_, err := ingress.Object[SubnetSpec, SubnetOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vpcId is required")
}

func TestProvision_MissingAvailabilityZoneFails(t *testing.T) {
	api := newFakeSubnetAPI()
	client := setupSubnetDriver(t, api)
	key := "vpc-123~public-a"
	spec := testSubnetSpec(key, "vpc-123", nil)
	spec.AvailabilityZone = ""

	_, err := ingress.Object[SubnetSpec, SubnetOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "availabilityZone is required")
}

func TestProvision_CidrConflictFails(t *testing.T) {
	api := newFakeSubnetAPI()
	api.createFunc = func(ctx context.Context, spec SubnetSpec) (string, error) {
		return "", &mockAPIError{code: "InvalidSubnet.Conflict", message: "overlap"}
	}
	client := setupSubnetDriver(t, api)
	key := "vpc-123~public-a"

	_, err := ingress.Object[SubnetSpec, SubnetOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSubnetSpec(key, "vpc-123", nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InvalidSubnet.Conflict")
}

func TestProvision_ConflictTaggedSubnetFails(t *testing.T) {
	api := newFakeSubnetAPI()
	api.managedKeys["vpc-123~public-a"] = "subnet-existing"
	client := setupSubnetDriver(t, api)
	key := "vpc-123~public-a"

	_, err := ingress.Object[SubnetSpec, SubnetOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSubnetSpec(key, "vpc-123", nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already managed by Praxis")
	assert.Equal(t, 0, api.createCalls)
}

func TestProvision_IdempotentReprovision(t *testing.T) {
	api := newFakeSubnetAPI()
	client := setupSubnetDriver(t, api)
	key := "vpc-123~public-a"
	spec := testSubnetSpec(key, "vpc-123", nil)

	out1, err := ingress.Object[SubnetSpec, SubnetOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	out2, err := ingress.Object[SubnetSpec, SubnetOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.SubnetId, out2.SubnetId)
	assert.Equal(t, 1, api.createCalls)
}

func TestProvision_MapPublicIpOnCreate(t *testing.T) {
	api := newFakeSubnetAPI()
	client := setupSubnetDriver(t, api)
	key := "vpc-123~public-a"
	spec := testSubnetSpec(key, "vpc-123", nil)
	spec.MapPublicIpOnLaunch = true

	outputs, err := ingress.Object[SubnetSpec, SubnetOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.True(t, outputs.MapPublicIpOnLaunch)
	assert.Equal(t, 1, api.modifyCalls)
	assert.Equal(t, []bool{true}, api.modifyValues)
}

func TestProvision_TagUpdate(t *testing.T) {
	api := newFakeSubnetAPI()
	client := setupSubnetDriver(t, api)
	key := "vpc-123~public-a"

	_, err := ingress.Object[SubnetSpec, SubnetOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSubnetSpec(key, "vpc-123", map[string]string{"Name": "public-a", "env": "dev"}))
	require.NoError(t, err)

	_, err = ingress.Object[SubnetSpec, SubnetOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSubnetSpec(key, "vpc-123", map[string]string{"Name": "public-a", "env": "prod"}))
	require.NoError(t, err)
	assert.Equal(t, 1, api.updateCalls)
	assert.Equal(t, "prod", api.observed["subnet-123"].Tags["env"])
}

func TestImport_ExistingSubnet(t *testing.T) {
	api := newFakeSubnetAPI()
	api.observed["subnet-import"] = ObservedState{
		SubnetId:            "subnet-import",
		VpcId:               "vpc-123",
		CidrBlock:           "10.0.2.0/24",
		AvailabilityZone:    "us-east-1a",
		AvailabilityZoneId:  "use1-az1",
		MapPublicIpOnLaunch: true,
		State:               "available",
		OwnerId:             "123456789012",
		AvailableIpCount:    251,
		Tags:                map[string]string{"Name": "imported"},
	}
	client := setupSubnetDriver(t, api)
	key := "us-east-1~subnet-import"

	outputs, err := ingress.Object[types.ImportRef, SubnetOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "subnet-import", Account: "test"})
	require.NoError(t, err)
	assert.Equal(t, "subnet-import", outputs.SubnetId)
	assert.True(t, outputs.MapPublicIpOnLaunch)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestDelete_ObservedModeBlocked(t *testing.T) {
	api := newFakeSubnetAPI()
	api.observed["subnet-import"] = ObservedState{
		SubnetId:         "subnet-import",
		VpcId:            "vpc-123",
		CidrBlock:        "10.0.2.0/24",
		AvailabilityZone: "us-east-1a",
		State:            "available",
		OwnerId:          "123456789012",
		Tags:             map[string]string{"Name": "imported"},
	}
	client := setupSubnetDriver(t, api)
	key := "us-east-1~subnet-import"

	_, err := ingress.Object[types.ImportRef, SubnetOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "subnet-import", Account: "test"})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Observed mode")
}

func TestDelete_DependencyViolationFails(t *testing.T) {
	api := newFakeSubnetAPI()
	client := setupSubnetDriver(t, api)
	key := "vpc-123~public-a"

	_, err := ingress.Object[SubnetSpec, SubnetOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testSubnetSpec(key, "vpc-123", nil))
	require.NoError(t, err)

	api.deleteFunc = func(ctx context.Context, subnetID string) error {
		return &mockAPIError{code: "DependencyViolation", message: "eni exists"}
	}

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dependent resources exist")
}

func TestReconcile_DetectsMapPublicIpDrift(t *testing.T) {
	api := newFakeSubnetAPI()
	client := setupSubnetDriver(t, api)
	key := "vpc-123~public-a"
	spec := testSubnetSpec(key, "vpc-123", nil)
	spec.MapPublicIpOnLaunch = true

	outputs, err := ingress.Object[SubnetSpec, SubnetOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.observed[outputs.SubnetId]
	obs.MapPublicIpOnLaunch = false
	api.observed[outputs.SubnetId] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Equal(t, 2, api.modifyCalls)
}

func TestReconcile_ObservedModeReportsOnly(t *testing.T) {
	api := newFakeSubnetAPI()
	api.observed["subnet-import"] = ObservedState{
		SubnetId:            "subnet-import",
		VpcId:               "vpc-123",
		CidrBlock:           "10.0.2.0/24",
		AvailabilityZone:    "us-east-1a",
		AvailabilityZoneId:  "use1-az1",
		MapPublicIpOnLaunch: false,
		State:               "available",
		OwnerId:             "123456789012",
		AvailableIpCount:    251,
		Tags:                map[string]string{"Name": "imported"},
	}
	client := setupSubnetDriver(t, api)
	key := "us-east-1~subnet-import"

	_, err := ingress.Object[types.ImportRef, SubnetOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "subnet-import", Account: "test"})
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.observed["subnet-import"]
	obs.MapPublicIpOnLaunch = true
	api.observed["subnet-import"] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.False(t, result.Correcting)
	assert.Equal(t, 0, api.modifyCalls)
}
