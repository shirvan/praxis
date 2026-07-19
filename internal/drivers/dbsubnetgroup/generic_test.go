package dbsubnetgroup

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

type subnetGroupDriftSink struct{}

func (subnetGroupDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }
func (subnetGroupDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error {
	return nil
}

type statefulSubnetGroupAPI struct {
	mu sync.Mutex

	exists   bool
	observed ObservedState
	creates  int
	reads    int
	updates  int
	deletes  int

	createErrors []error
}

func (f *statefulSubnetGroupAPI) CreateDBSubnetGroup(_ context.Context, spec DBSubnetGroupSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exists {
		return "", &mockAPIError{code: "DBSubnetGroupAlreadyExistsFault", message: "already exists"}
	}
	f.exists = true
	f.creates++
	f.observed = ObservedState{
		GroupName: spec.GroupName, ARN: "arn:aws:rds:us-east-1:123456789012:subgrp:" + spec.GroupName,
		Description: spec.Description, VpcId: "vpc-123", SubnetIds: normalizeStrings(spec.SubnetIds),
		AvailabilityZones: []string{"us-east-1a", "us-east-1b"}, Status: "Complete",
		Tags: maps.Clone(spec.Tags), ManagedKey: spec.ManagedKey,
	}
	if len(f.createErrors) > 0 {
		err := f.createErrors[0]
		f.createErrors = f.createErrors[1:]
		return "", err
	}
	return f.observed.ARN, nil
}

func (f *statefulSubnetGroupAPI) DescribeDBSubnetGroup(_ context.Context, groupName string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if !f.exists || f.observed.GroupName != groupName {
		return ObservedState{}, awserr.NotFound("db subnet group " + groupName + " not found")
	}
	return cloneSubnetGroupObserved(f.observed), nil
}

func (f *statefulSubnetGroupAPI) ModifyDBSubnetGroup(_ context.Context, spec DBSubnetGroupSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.GroupName != spec.GroupName {
		return awserr.NotFound("db subnet group not found")
	}
	f.observed.Description = spec.Description
	f.observed.SubnetIds = normalizeStrings(spec.SubnetIds)
	f.updates++
	return nil
}

func (f *statefulSubnetGroupAPI) DeleteDBSubnetGroup(_ context.Context, groupName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.GroupName != groupName {
		return awserr.NotFound("db subnet group not found")
	}
	f.exists = false
	f.observed = ObservedState{}
	f.deletes++
	return nil
}

func (f *statefulSubnetGroupAPI) UpdateTags(_ context.Context, arn string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.ARN != arn {
		return awserr.NotFound("db subnet group not found")
	}
	f.observed.Tags = maps.Clone(tags)
	f.updates++
	return nil
}

func (f *statefulSubnetGroupAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulSubnetGroupAPI) removeExternally() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exists = false
	f.observed = ObservedState{}
}

func cloneSubnetGroupObserved(in ObservedState) ObservedState {
	out := in
	out.SubnetIds = append([]string(nil), in.SubnetIds...)
	out.AvailabilityZones = append([]string(nil), in.AvailabilityZones...)
	out.Tags = maps.Clone(in.Tags)
	return out
}

func setupGenericDBSubnetGroup(t *testing.T, api DBSubnetGroupAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericDBSubnetGroupDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) DBSubnetGroupAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(subnetGroupDriftSink{})).Ingress()
}

func managedSubnetGroupSpec() DBSubnetGroupSpec {
	return DBSubnetGroupSpec{
		Account: "test", Region: "us-east-1", GroupName: "generic-subnet-group",
		Description: "generic test", SubnetIds: []string{"subnet-b", "subnet-a"},
		Tags: map[string]string{"env": "test"},
	}
}

func TestGenericDBSubnetGroupCoreLifecycle(t *testing.T) {
	api := &statefulSubnetGroupAPI{}
	client := setupGenericDBSubnetGroup(t, api)
	key := "us-east-1~generic-subnet-group"
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[DBSubnetGroupSpec, DBSubnetGroupOutputs]{
		Client: client, ServiceName: ServiceName, Key: key, Spec: managedSubnetGroupSpec(), Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs DBSubnetGroupSpec) {
			assert.Equal(t, key, inputs.ManagedKey)
			assert.Equal(t, []string{"subnet-a", "subnet-b"}, inputs.SubnetIds)
			assert.Equal(t, managedSubnetGroupSpec().Tags, inputs.Tags)
		},
	})
}

func TestGenericDBSubnetGroupObservedImportLifecycle(t *testing.T) {
	api := &statefulSubnetGroupAPI{exists: true, observed: ObservedState{
		GroupName: "existing-subnet-group", ARN: "arn:existing", Description: "existing",
		VpcId: "vpc-1", SubnetIds: []string{"subnet-a", "subnet-b"},
		AvailabilityZones: []string{"us-east-1a", "us-east-1b"}, Status: "Complete",
		Tags: map[string]string{"env": "import"},
	}}
	client := setupGenericDBSubnetGroup(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[DBSubnetGroupOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~existing-subnet-group",
		Ref: types.ImportRef{Account: "test", ResourceID: "existing-subnet-group"}, Snapshot: api.snapshot,
	})
}

func TestGenericDBSubnetGroupRecoversAmbiguousCreate(t *testing.T) {
	api := &statefulSubnetGroupAPI{createErrors: []error{errors.New("request timeout")}}
	client := setupGenericDBSubnetGroup(t, api)
	_, err := ingress.Object[types.ProvisionRequest, DBSubnetGroupOutputs](
		client, ServiceName, "us-east-1~generic-subnet-group", "Provision",
	).Request(t.Context(), drivertest.ProvisionRequest(t, managedSubnetGroupSpec()))
	require.NoError(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericDBSubnetGroupRejectsNameCollisionWithoutOwnership(t *testing.T) {
	spec := managedSubnetGroupSpec()
	api := &statefulSubnetGroupAPI{exists: true, observed: ObservedState{
		GroupName: spec.GroupName, ARN: "arn:foreign", Description: spec.Description,
		SubnetIds: spec.SubnetIds, Tags: spec.Tags,
	}}
	client := setupGenericDBSubnetGroup(t, api)
	_, err := ingress.Object[types.ProvisionRequest, DBSubnetGroupOutputs](
		client, ServiceName, "us-east-1~generic-subnet-group", "Provision",
	).Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exact Praxis ownership")
	assert.Zero(t, api.snapshot().Creates)
}

func TestGenericDBSubnetGroupRejectsImmutableNameChange(t *testing.T) {
	api := &statefulSubnetGroupAPI{}
	client := setupGenericDBSubnetGroup(t, api)
	key := "us-east-1~generic-subnet-group"
	spec := managedSubnetGroupSpec()
	_, err := ingress.Object[types.ProvisionRequest, DBSubnetGroupOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	before := api.snapshot()
	spec.GroupName = "replacement-name"
	_, err = ingress.Object[types.ProvisionRequest, DBSubnetGroupOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "groupName is immutable")
	assert.Equal(t, before.Creates, api.snapshot().Creates)
}

func TestGenericDBSubnetGroupExternalDeleteRequiresReplacement(t *testing.T) {
	api := &statefulSubnetGroupAPI{}
	client := setupGenericDBSubnetGroup(t, api)
	key := "us-east-1~generic-subnet-group"
	_, err := ingress.Object[types.ProvisionRequest, DBSubnetGroupOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedSubnetGroupSpec()))
	require.NoError(t, err)
	before := api.snapshot()
	api.removeExternally()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "deleted externally")
	assert.Equal(t, before.Creates, api.snapshot().Creates)
}
