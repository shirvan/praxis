package auroracluster

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

type statefulClusterAPI struct {
	mu                               sync.Mutex
	resources                        map[string]ObservedState
	creates, reads, updates, deletes int
	passwordWrites                   []string
}

func newStatefulClusterAPI() *statefulClusterAPI {
	return &statefulClusterAPI{resources: map[string]ObservedState{}}
}
func (f *statefulClusterAPI) CreateDBCluster(_ context.Context, s AuroraClusterSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.resources[s.ClusterIdentifier]; ok {
		return "", &smithy.GenericAPIError{Code: "DBClusterAlreadyExistsFault"}
	}
	f.creates++
	o := observedCluster(s)
	f.resources[s.ClusterIdentifier] = o
	return o.ARN, nil
}
func (f *statefulClusterAPI) DescribeDBCluster(_ context.Context, id string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	o, ok := f.resources[id]
	if !ok {
		return ObservedState{}, &smithy.GenericAPIError{Code: "DBClusterNotFoundFault"}
	}
	o.Tags = maps.Clone(o.Tags)
	return o, nil
}
func (f *statefulClusterAPI) ModifyDBCluster(_ context.Context, s AuroraClusterSpec, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.resources[s.ClusterIdentifier]
	if !ok {
		return &smithy.GenericAPIError{Code: "DBClusterNotFoundFault"}
	}
	f.updates++
	f.passwordWrites = append(f.passwordWrites, s.MasterUserPassword)
	updated := observedCluster(s)
	updated.ARN, updated.ClusterResourceId, updated.Endpoint, updated.ReaderEndpoint, updated.Tags = current.ARN, current.ClusterResourceId, current.Endpoint, current.ReaderEndpoint, current.Tags
	f.resources[s.ClusterIdentifier] = updated
	return nil
}
func (f *statefulClusterAPI) DeleteDBCluster(_ context.Context, id string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.resources[id]; !ok {
		return &smithy.GenericAPIError{Code: "DBClusterNotFoundFault"}
	}
	f.deletes++
	delete(f.resources, id)
	return nil
}
func (f *statefulClusterAPI) WaitUntilAvailable(context.Context, string) error { return nil }
func (f *statefulClusterAPI) WaitUntilDeleted(context.Context, string) error   { return nil }
func (f *statefulClusterAPI) UpdateTags(_ context.Context, arn string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name := range f.resources {
		o := f.resources[name]
		if o.ARN == arn {
			f.updates++
			managed := o.Tags[managedKeyTag]
			o.Tags = maps.Clone(tags)
			if managed != "" {
				o.Tags[managedKeyTag] = managed
			}
			f.resources[name] = o
			return nil
		}
	}
	return &smithy.GenericAPIError{Code: "DBClusterNotFoundFault"}
}
func (f *statefulClusterAPI) ListTags(_ context.Context, arn string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name := range f.resources {
		o := f.resources[name]
		if o.ARN == arn {
			return maps.Clone(o.Tags), nil
		}
	}
	return nil, &smithy.GenericAPIError{Code: "DBClusterNotFoundFault"}
}
func (f *statefulClusterAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}
func (f *statefulClusterAPI) seed(o ObservedState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resources[o.ClusterIdentifier] = o
}
func (f *statefulClusterAPI) force(n string, fn func(*ObservedState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.resources[n]
	fn(&o)
	f.resources[n] = o
}
func (f *statefulClusterAPI) passwords() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.passwordWrites...)
}
func observedCluster(s AuroraClusterSpec) ObservedState {
	return ObservedState{ClusterIdentifier: s.ClusterIdentifier, ClusterResourceId: "cluster-1", ARN: "arn:aws:rds:us-east-1:123:cluster:" + s.ClusterIdentifier, Engine: s.Engine, EngineVersion: s.EngineVersion, MasterUsername: s.MasterUsername, DatabaseName: s.DatabaseName, Port: s.Port, DBSubnetGroupName: s.DBSubnetGroupName, DBClusterParameterGroupName: s.DBClusterParameterGroupName, VpcSecurityGroupIds: append([]string(nil), s.VpcSecurityGroupIds...), StorageEncrypted: s.StorageEncrypted, KMSKeyId: s.KMSKeyId, BackupRetentionPeriod: s.BackupRetentionPeriod, PreferredBackupWindow: s.PreferredBackupWindow, PreferredMaintenanceWindow: s.PreferredMaintenanceWindow, DeletionProtection: s.DeletionProtection, EnabledCloudwatchLogsExports: append([]string(nil), s.EnabledCloudwatchLogsExports...), Endpoint: s.ClusterIdentifier + ".test", ReaderEndpoint: s.ClusterIdentifier + "-ro.test", Status: "available", Tags: map[string]string{"env": "test", managedKeyTag: s.ManagedKey}}
}

type clusterSink struct{}

func (clusterSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (clusterSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }
func setupCluster(t *testing.T, api AuroraClusterAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericAuroraClusterDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) AuroraClusterAPI { return api })
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(clusterSink{})).Ingress()
}
func clusterSpec(name, password string) AuroraClusterSpec {
	return applyDefaults(AuroraClusterSpec{Account: "test", Region: "us-east-1", ClusterIdentifier: name, Engine: "aurora-mysql", EngineVersion: "8.0", MasterUsername: "admin", MasterUserPassword: password, Port: 3306, Tags: map[string]string{"env": "test"}})
}
func TestGenericAuroraClusterCoreLifecycle(t *testing.T) {
	api := newStatefulClusterAPI()
	client := setupCluster(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[AuroraClusterSpec, AuroraClusterOutputs]{Client: client, ServiceName: ServiceName, Key: "us-east-1~core", Spec: clusterSpec("core", "old-secret"), Snapshot: api.snapshot, AssertInputs: func(t *testing.T, got AuroraClusterSpec) { assert.Equal(t, "us-east-1~core", got.ManagedKey) }})
}
func TestGenericAuroraClusterObservedImport(t *testing.T) {
	api := newStatefulClusterAPI()
	api.seed(observedCluster(clusterSpec("imported", "")))
	client := setupCluster(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[AuroraClusterOutputs]{Client: client, ServiceName: ServiceName, Key: "us-east-1~imported", Ref: types.ImportRef{ResourceID: "imported", Account: "test"}, Snapshot: api.snapshot})
}
func TestGenericAuroraClusterPasswordChangeIsProvisionOnly(t *testing.T) {
	api := newStatefulClusterAPI()
	client := setupCluster(t, api)
	key := "us-east-1~password"
	spec := clusterSpec("password", "old-secret")
	_, err := ingress.Object[types.ProvisionRequest, AuroraClusterOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	spec.MasterUserPassword = "new-secret"
	_, err = ingress.Object[types.ProvisionRequest, AuroraClusterOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, []string{"new-secret"}, api.passwords())
	api.force("password", func(o *ObservedState) { o.EngineVersion = "8.1" })
	_, err = ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, []string{"new-secret", ""}, api.passwords(), "Reconcile may converge visible fields but never sends the write-only password")
}

func TestGenericAuroraClusterImmutableOnlyChangeKeepsPriorAcceptedInputs(t *testing.T) {
	api := newStatefulClusterAPI()
	client := setupCluster(t, api)
	key := "us-east-1~immutable"
	accepted := clusterSpec("immutable", "secret")
	_, err := ingress.Object[types.ProvisionRequest, AuroraClusterOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, accepted))
	require.NoError(t, err)
	changed := accepted
	changed.Engine = "aurora-postgresql"
	_, err = ingress.Object[types.ProvisionRequest, AuroraClusterOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, changed))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")
	stored, err := ingress.Object[restate.Void, AuroraClusterSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, accepted.Engine, stored.Engine)
}
