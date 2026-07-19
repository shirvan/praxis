package dbparametergroup

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

type parameterGroupDriftSink struct{}

func (parameterGroupDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }
func (parameterGroupDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error {
	return nil
}

type statefulParameterGroupAPI struct {
	mu sync.Mutex

	exists   bool
	observed ObservedState
	creates  int
	reads    int
	updates  int
	deletes  int

	createErrors []error
}

func (f *statefulParameterGroupAPI) CreateParameterGroup(_ context.Context, spec DBParameterGroupSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exists {
		return "", &mockAPIError{code: "DBParameterGroupAlreadyExistsFault", message: "already exists"}
	}
	f.exists = true
	f.creates++
	f.observed = ObservedState{
		GroupName: spec.GroupName, ARN: "arn:aws:rds:us-east-1:123456789012:pg:" + spec.GroupName,
		Family: spec.Family, Type: spec.Type, Description: spec.Description,
		Parameters: map[string]string{}, Tags: maps.Clone(spec.Tags), ManagedKey: spec.ManagedKey,
	}
	if len(f.createErrors) > 0 {
		err := f.createErrors[0]
		f.createErrors = f.createErrors[1:]
		return "", err
	}
	return f.observed.ARN, nil
}

func (f *statefulParameterGroupAPI) DescribeParameterGroup(_ context.Context, groupName, groupType string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if !f.exists || f.observed.GroupName != groupName || f.observed.Type != groupType {
		return ObservedState{}, awserr.NotFound("db parameter group " + groupName + " not found")
	}
	return cloneParameterGroupObserved(f.observed), nil
}

func (f *statefulParameterGroupAPI) UpdateParameters(_ context.Context, spec DBParameterGroupSpec, _ ObservedState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.GroupName != spec.GroupName || f.observed.Type != spec.Type {
		return awserr.NotFound("db parameter group not found")
	}
	f.observed.Parameters = maps.Clone(spec.Parameters)
	f.updates++
	return nil
}

func (f *statefulParameterGroupAPI) DeleteParameterGroup(_ context.Context, groupName, groupType string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.GroupName != groupName || f.observed.Type != groupType {
		return awserr.NotFound("db parameter group not found")
	}
	f.exists = false
	f.observed = ObservedState{}
	f.deletes++
	return nil
}

func (f *statefulParameterGroupAPI) UpdateTags(_ context.Context, arn string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.ARN != arn {
		return awserr.NotFound("db parameter group not found")
	}
	f.observed.Tags = maps.Clone(tags)
	f.updates++
	return nil
}

func (f *statefulParameterGroupAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulParameterGroupAPI) removeExternally() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exists = false
	f.observed = ObservedState{}
}

func cloneParameterGroupObserved(in ObservedState) ObservedState {
	out := in
	out.Parameters = maps.Clone(in.Parameters)
	out.Tags = maps.Clone(in.Tags)
	return out
}

func setupGenericDBParameterGroup(t *testing.T, api DBParameterGroupAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericDBParameterGroupDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) DBParameterGroupAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(parameterGroupDriftSink{})).Ingress()
}

func managedParameterGroupSpec() DBParameterGroupSpec {
	return DBParameterGroupSpec{
		Account: "test", Region: "us-east-1", GroupName: "generic-parameter-group",
		Type: TypeDB, Family: "mysql8.0", Description: "generic test",
		Parameters: map[string]string{"max_connections": "100"}, Tags: map[string]string{"env": "test"},
	}
}

func TestGenericDBParameterGroupCoreLifecycle(t *testing.T) {
	api := &statefulParameterGroupAPI{}
	client := setupGenericDBParameterGroup(t, api)
	key := "us-east-1~generic-parameter-group"
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[DBParameterGroupSpec, DBParameterGroupOutputs]{
		Client: client, ServiceName: ServiceName, Key: key, Spec: managedParameterGroupSpec(), Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs DBParameterGroupSpec) {
			assert.Equal(t, key, inputs.ManagedKey)
			assert.Equal(t, managedParameterGroupSpec().Parameters, inputs.Parameters)
			assert.Equal(t, managedParameterGroupSpec().Tags, inputs.Tags)
		},
	})
}

func TestGenericDBParameterGroupObservedImportLifecycle(t *testing.T) {
	api := &statefulParameterGroupAPI{exists: true, observed: ObservedState{
		GroupName: "existing-parameter-group", ARN: "arn:existing", Type: TypeDB,
		Family: "mysql8.0", Description: "existing", Parameters: map[string]string{"max_connections": "50"},
		Tags: map[string]string{"env": "import"},
	}}
	client := setupGenericDBParameterGroup(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[DBParameterGroupOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~existing-parameter-group",
		Ref: types.ImportRef{Account: "test", ResourceID: "existing-parameter-group"}, Snapshot: api.snapshot,
	})
}

func TestGenericDBParameterGroupRecoversAmbiguousCreate(t *testing.T) {
	api := &statefulParameterGroupAPI{createErrors: []error{errors.New("request timeout")}}
	client := setupGenericDBParameterGroup(t, api)
	_, err := ingress.Object[types.ProvisionRequest, DBParameterGroupOutputs](
		client, ServiceName, "us-east-1~generic-parameter-group", "Provision",
	).Request(t.Context(), drivertest.ProvisionRequest(t, managedParameterGroupSpec()))
	require.NoError(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericDBParameterGroupRejectsNameCollisionWithoutOwnership(t *testing.T) {
	spec := managedParameterGroupSpec()
	api := &statefulParameterGroupAPI{exists: true, observed: ObservedState{
		GroupName: spec.GroupName, ARN: "arn:foreign", Type: spec.Type, Family: spec.Family,
		Description: spec.Description, Parameters: maps.Clone(spec.Parameters), Tags: maps.Clone(spec.Tags),
	}}
	client := setupGenericDBParameterGroup(t, api)
	_, err := ingress.Object[types.ProvisionRequest, DBParameterGroupOutputs](
		client, ServiceName, "us-east-1~generic-parameter-group", "Provision",
	).Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exact Praxis ownership")
	assert.Zero(t, api.snapshot().Creates)
}

func TestGenericDBParameterGroupRejectsImmutableFamilyChange(t *testing.T) {
	api := &statefulParameterGroupAPI{}
	client := setupGenericDBParameterGroup(t, api)
	key := "us-east-1~generic-parameter-group"
	spec := managedParameterGroupSpec()
	_, err := ingress.Object[types.ProvisionRequest, DBParameterGroupOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	before := api.snapshot()
	spec.Family = "postgres16"
	_, err = ingress.Object[types.ProvisionRequest, DBParameterGroupOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "family is immutable")
	assert.Equal(t, before.Creates, api.snapshot().Creates)
}

func TestGenericDBParameterGroupExternalDeleteRequiresReplacement(t *testing.T) {
	api := &statefulParameterGroupAPI{}
	client := setupGenericDBParameterGroup(t, api)
	key := "us-east-1~generic-parameter-group"
	_, err := ingress.Object[types.ProvisionRequest, DBParameterGroupOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedParameterGroupSpec()))
	require.NoError(t, err)
	before := api.snapshot()
	api.removeExternally()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "deleted externally")
	assert.Equal(t, before.Creates, api.snapshot().Creates)
}
