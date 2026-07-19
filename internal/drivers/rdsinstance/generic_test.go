package rdsinstance

import (
	"context"
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

type statefulInstanceAPI struct {
	mu                               sync.Mutex
	resources                        map[string]ObservedState
	creates, reads, updates, deletes int
	passwordWrites                   []string
}

func newStatefulInstanceAPI() *statefulInstanceAPI {
	return &statefulInstanceAPI{resources: map[string]ObservedState{}}
}
func (f *statefulInstanceAPI) CreateDBInstance(_ context.Context, spec RDSInstanceSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.resources[spec.DBIdentifier]; ok {
		return "", &smithy.GenericAPIError{Code: "DBInstanceAlreadyExists"}
	}
	f.creates++
	observed := observedInstance(spec)
	f.resources[spec.DBIdentifier] = observed
	return observed.ARN, nil
}
func (f *statefulInstanceAPI) DescribeDBInstance(_ context.Context, id string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	observed, ok := f.resources[id]
	if !ok {
		return ObservedState{}, &smithy.GenericAPIError{Code: "DBInstanceNotFound"}
	}
	observed.Tags = maps.Clone(observed.Tags)
	return observed, nil
}
func (f *statefulInstanceAPI) ModifyDBInstance(_ context.Context, spec RDSInstanceSpec, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.resources[spec.DBIdentifier]
	if !ok {
		return &smithy.GenericAPIError{Code: "DBInstanceNotFound"}
	}
	f.updates++
	f.passwordWrites = append(f.passwordWrites, spec.MasterUserPassword)
	updated := observedInstance(spec)
	updated.ARN, updated.DbiResourceId, updated.Endpoint, updated.Tags = current.ARN, current.DbiResourceId, current.Endpoint, current.Tags
	f.resources[spec.DBIdentifier] = updated
	return nil
}
func (f *statefulInstanceAPI) DeleteDBInstance(_ context.Context, id string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.resources[id]; !ok {
		return &smithy.GenericAPIError{Code: "DBInstanceNotFound"}
	}
	f.deletes++
	delete(f.resources, id)
	return nil
}
func (f *statefulInstanceAPI) WaitUntilAvailable(context.Context, string) error { return nil }
func (f *statefulInstanceAPI) WaitUntilDeleted(context.Context, string) error   { return nil }
func (f *statefulInstanceAPI) UpdateTags(_ context.Context, arn string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name := range f.resources {
		observed := f.resources[name]
		if observed.ARN == arn {
			f.updates++
			managed := observed.Tags[managedKeyTag]
			observed.Tags = maps.Clone(tags)
			if managed != "" {
				observed.Tags[managedKeyTag] = managed
			}
			f.resources[name] = observed
			return nil
		}
	}
	return &smithy.GenericAPIError{Code: "DBInstanceNotFound"}
}
func (f *statefulInstanceAPI) ListTags(_ context.Context, arn string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name := range f.resources {
		observed := f.resources[name]
		if observed.ARN == arn {
			return maps.Clone(observed.Tags), nil
		}
	}
	return nil, &smithy.GenericAPIError{Code: "DBInstanceNotFound"}
}
func (f *statefulInstanceAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}
func (f *statefulInstanceAPI) seed(observed ObservedState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resources[observed.DBIdentifier] = observed
}
func (f *statefulInstanceAPI) force(name string, fn func(*ObservedState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed := f.resources[name]
	fn(&observed)
	f.resources[name] = observed
}
func (f *statefulInstanceAPI) passwords() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.passwordWrites...)
}
func observedInstance(spec RDSInstanceSpec) ObservedState {
	return ObservedState{DBIdentifier: spec.DBIdentifier, DbiResourceId: "db-1", ARN: "arn:aws:rds:us-east-1:123:db:" + spec.DBIdentifier, Engine: spec.Engine, EngineVersion: spec.EngineVersion, InstanceClass: spec.InstanceClass, AllocatedStorage: spec.AllocatedStorage, StorageType: spec.StorageType, IOPS: spec.IOPS, StorageThroughput: spec.StorageThroughput, StorageEncrypted: spec.StorageEncrypted, KMSKeyId: spec.KMSKeyId, MasterUsername: spec.MasterUsername, DBSubnetGroupName: spec.DBSubnetGroupName, ParameterGroupName: spec.ParameterGroupName, VpcSecurityGroupIds: append([]string(nil), spec.VpcSecurityGroupIds...), DBClusterIdentifier: spec.DBClusterIdentifier, MultiAZ: spec.MultiAZ, PubliclyAccessible: spec.PubliclyAccessible, BackupRetentionPeriod: spec.BackupRetentionPeriod, PreferredBackupWindow: spec.PreferredBackupWindow, PreferredMaintenanceWindow: spec.PreferredMaintenanceWindow, DeletionProtection: spec.DeletionProtection, AutoMinorVersionUpgrade: spec.AutoMinorVersionUpgrade, MonitoringInterval: spec.MonitoringInterval, MonitoringRoleArn: spec.MonitoringRoleArn, PerformanceInsightsEnabled: spec.PerformanceInsightsEnabled, Endpoint: spec.DBIdentifier + ".test", Port: 3306, Status: "available", Tags: map[string]string{"env": "test", managedKeyTag: spec.ManagedKey}}
}

type instanceSink struct{}

func (instanceSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (instanceSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }
func setupInstance(t *testing.T, api RDSInstanceAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericRDSInstanceDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) RDSInstanceAPI { return api })
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(instanceSink{})).Ingress()
}
func instanceSpec(name, password string) RDSInstanceSpec {
	return applyDefaults(RDSInstanceSpec{Account: "test", Region: "us-east-1", DBIdentifier: name, Engine: "mysql", EngineVersion: "8.0", InstanceClass: "db.t3.micro", AllocatedStorage: 20, MasterUsername: "admin", MasterUserPassword: password, Tags: map[string]string{"env": "test"}})
}
func TestGenericRDSInstanceCoreLifecycle(t *testing.T) {
	api := newStatefulInstanceAPI()
	client := setupInstance(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[RDSInstanceSpec, RDSInstanceOutputs]{Client: client, ServiceName: ServiceName, Key: "us-east-1~core", Spec: instanceSpec("core", "old-secret"), Snapshot: api.snapshot, AssertInputs: func(t *testing.T, got RDSInstanceSpec) { assert.Equal(t, "us-east-1~core", got.ManagedKey) }})
}
func TestGenericRDSInstanceObservedImport(t *testing.T) {
	api := newStatefulInstanceAPI()
	api.seed(observedInstance(instanceSpec("imported", "")))
	client := setupInstance(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[RDSInstanceOutputs]{Client: client, ServiceName: ServiceName, Key: "us-east-1~imported", Ref: types.ImportRef{ResourceID: "imported", Account: "test"}, Snapshot: api.snapshot})
}
func TestGenericRDSInstancePasswordChangeIsProvisionOnly(t *testing.T) {
	api := newStatefulInstanceAPI()
	client := setupInstance(t, api)
	key := "us-east-1~password"
	spec := instanceSpec("password", "old-secret")
	_, err := ingress.Object[types.ProvisionRequest, RDSInstanceOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	spec.MasterUserPassword = "new-secret"
	_, err = ingress.Object[types.ProvisionRequest, RDSInstanceOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, []string{"new-secret"}, api.passwords())
	api.force("password", func(o *ObservedState) { o.InstanceClass = "db.t3.small" })
	_, err = ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, []string{"new-secret", ""}, api.passwords(), "Reconcile may converge visible fields but never sends the write-only password")
}

func TestGenericRDSInstanceImmutableOnlyChangeKeepsPriorAcceptedInputs(t *testing.T) {
	api := newStatefulInstanceAPI()
	client := setupInstance(t, api)
	key := "us-east-1~immutable"
	accepted := instanceSpec("immutable", "secret")
	_, err := ingress.Object[types.ProvisionRequest, RDSInstanceOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, accepted))
	require.NoError(t, err)
	changed := accepted
	changed.Engine = "postgres"
	_, err = ingress.Object[types.ProvisionRequest, RDSInstanceOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, changed))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")
	stored, err := ingress.Object[restate.Void, RDSInstanceSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, accepted.Engine, stored.Engine)
}
