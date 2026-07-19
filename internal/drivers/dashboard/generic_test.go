package dashboard

import (
	"context"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type statefulDashboardAPI struct {
	mu       sync.Mutex
	observed ObservedState
	puts     int
	reads    int
	deletes  int
}

func (f *statefulDashboardAPI) PutDashboard(_ context.Context, spec DashboardSpec) ([]ValidationMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	f.observed = ObservedState{
		DashboardArn:  "arn:aws:cloudwatch::123456789012:dashboard/" + spec.DashboardName,
		DashboardName: spec.DashboardName,
		DashboardBody: spec.DashboardBody,
	}
	return nil, nil
}

func (f *statefulDashboardAPI) GetDashboard(_ context.Context, name string) (ObservedState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.observed, f.observed.DashboardName == name, nil
}

func (f *statefulDashboardAPI) DeleteDashboard(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	f.observed = ObservedState{}
	return nil
}

func (f *statefulDashboardAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.puts, Reads: f.reads, Updates: f.puts, Deletes: f.deletes}
}

func setupGenericDashboard(t *testing.T, api DashboardAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := NewGenericDashboardDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) DashboardAPI { return api })
	return restatetest.Start(t, restate.Reflect(driver)).Ingress()
}

func TestGenericDashboardCoreLifecycle(t *testing.T) {
	api := &statefulDashboardAPI{}
	client := setupGenericDashboard(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[DashboardSpec, DashboardOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~generic-dashboard",
		Spec:     DashboardSpec{Account: "test", Region: "us-east-1", DashboardName: "generic-dashboard", DashboardBody: `{"widgets":[]}`},
		Snapshot: api.snapshot,
	})
}

func TestGenericDashboardObservedImportLifecycle(t *testing.T) {
	api := &statefulDashboardAPI{observed: ObservedState{
		DashboardArn: "arn:aws:cloudwatch::123456789012:dashboard/existing", DashboardName: "existing", DashboardBody: `{"widgets":[]}`,
	}}
	client := setupGenericDashboard(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[DashboardOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~existing",
		Ref: types.ImportRef{ResourceID: "existing", Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericDashboardRejectsImmutableNameAndRetainsInputs(t *testing.T) {
	api := &statefulDashboardAPI{}
	client := setupGenericDashboard(t, api)
	key := "us-east-1~immutable-dashboard"
	spec := DashboardSpec{Account: "test", Region: "us-east-1", DashboardName: "immutable-dashboard", DashboardBody: `{"widgets":[]}`}

	_, err := ingress.Object[DashboardSpec, DashboardOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	accepted, err := ingress.Object[restate.Void, DashboardSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	changed := accepted
	changed.DashboardName = "different-dashboard"
	_, err = ingress.Object[DashboardSpec, DashboardOutputs](client, ServiceName, key, "Provision").Request(t.Context(), changed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dashboardName is immutable")

	retained, err := ingress.Object[restate.Void, DashboardSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, accepted, retained)
}
