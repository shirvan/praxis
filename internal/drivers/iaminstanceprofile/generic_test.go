package iaminstanceprofile

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
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	"github.com/shirvan/praxis/internal/eventing"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type roleCall struct {
	ProfileName string
	RoleName    string
}

type statefulIAMInstanceProfileAPI struct {
	mu sync.Mutex

	profiles                 map[string]ObservedState
	creates                  int
	reads                    int
	updates                  int
	deletes                  int
	addRoleCalls             []roleCall
	removeRoleCalls          []roleCall
	failAddRoleOnce          bool
	failAddRoleTransientOnce bool
	deleteConflict           bool
}

func newStatefulIAMInstanceProfileAPI() *statefulIAMInstanceProfileAPI {
	return &statefulIAMInstanceProfileAPI{profiles: map[string]ObservedState{}}
}

func (f *statefulIAMInstanceProfileAPI) CreateInstanceProfile(_ context.Context, spec IAMInstanceProfileSpec) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.profiles[spec.InstanceProfileName]; exists {
		return "", "", errors.New("EntityAlreadyExists: instance profile already exists")
	}
	f.creates++
	observed := ObservedState{
		Arn:                 "arn:aws:iam::123456789012:instance-profile" + spec.Path + spec.InstanceProfileName,
		InstanceProfileId:   "AIPAEXAMPLE",
		InstanceProfileName: spec.InstanceProfileName,
		Path:                spec.Path,
		Tags:                cloneTags(spec.Tags),
		CreateDate:          "2026-07-17T00:00:00Z",
	}
	f.profiles[spec.InstanceProfileName] = observed
	return observed.Arn, observed.InstanceProfileId, nil
}

func (f *statefulIAMInstanceProfileAPI) DescribeInstanceProfile(_ context.Context, name string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	observed, exists := f.profiles[name]
	if !exists {
		return ObservedState{}, errors.New("NoSuchEntity: instance profile does not exist")
	}
	return cloneObservedState(observed), nil
}

func (f *statefulIAMInstanceProfileAPI) DeleteInstanceProfile(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	observed, exists := f.profiles[name]
	if !exists {
		return errors.New("NoSuchEntity: instance profile does not exist")
	}
	if observed.RoleName != "" || f.deleteConflict {
		return errors.New("DeleteConflict: instance profile still has an attached resource")
	}
	delete(f.profiles, name)
	return nil
}

func (f *statefulIAMInstanceProfileAPI) AddRoleToInstanceProfile(_ context.Context, name, roleName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addRoleCalls = append(f.addRoleCalls, roleCall{ProfileName: name, RoleName: roleName})
	if f.failAddRoleTransientOnce {
		f.failAddRoleTransientOnce = false
		return errors.New("Throttling: injected retryable role association failure")
	}
	if f.failAddRoleOnce {
		f.failAddRoleOnce = false
		return errors.New("NoSuchEntity: injected role lookup failure")
	}
	observed, exists := f.profiles[name]
	if !exists {
		return errors.New("NoSuchEntity: instance profile does not exist")
	}
	if observed.RoleName != "" {
		return errors.New("LimitExceeded: instance profile already has a role")
	}
	f.updates++
	observed.RoleName = roleName
	f.profiles[name] = observed
	return nil
}

func (f *statefulIAMInstanceProfileAPI) RemoveRoleFromInstanceProfile(_ context.Context, name, roleName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeRoleCalls = append(f.removeRoleCalls, roleCall{ProfileName: name, RoleName: roleName})
	observed, exists := f.profiles[name]
	if !exists {
		return errors.New("NoSuchEntity: instance profile does not exist")
	}
	if observed.RoleName == "" || observed.RoleName != roleName {
		return errors.New("NoSuchEntity: role association does not exist")
	}
	f.updates++
	observed.RoleName = ""
	f.profiles[name] = observed
	return nil
}

func (f *statefulIAMInstanceProfileAPI) TagInstanceProfile(_ context.Context, name string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed, exists := f.profiles[name]
	if !exists {
		return errors.New("NoSuchEntity: instance profile does not exist")
	}
	f.updates++
	if observed.Tags == nil {
		observed.Tags = map[string]string{}
	}
	maps.Copy(observed.Tags, tags)
	f.profiles[name] = observed
	return nil
}

func (f *statefulIAMInstanceProfileAPI) UntagInstanceProfile(_ context.Context, name string, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed, exists := f.profiles[name]
	if !exists {
		return errors.New("NoSuchEntity: instance profile does not exist")
	}
	f.updates++
	for _, key := range keys {
		delete(observed.Tags, key)
	}
	f.profiles[name] = observed
	return nil
}

func (f *statefulIAMInstanceProfileAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulIAMInstanceProfileAPI) profile(name string) ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneObservedState(f.profiles[name])
}

func (f *statefulIAMInstanceProfileAPI) setProfile(observed ObservedState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profiles[observed.InstanceProfileName] = cloneObservedState(observed)
}

func (f *statefulIAMInstanceProfileAPI) forceRoleAndTagDrift(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed := f.profiles[name]
	observed.RoleName = "wrong-role"
	observed.Tags = map[string]string{"env": "wrong", "stale": "remove", "praxis:managed-key": "internal"}
	f.profiles[name] = observed
}

type iamInstanceProfileDriftSink struct{}

func (iamInstanceProfileDriftSink) ServiceName() string {
	return eventing.ResourceEventBridgeServiceName
}

func (iamInstanceProfileDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error {
	return nil
}

func setupGenericIAMInstanceProfile(t *testing.T, api IAMInstanceProfileAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericIAMInstanceProfileDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) IAMInstanceProfileAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(iamInstanceProfileDriftSink{})).Ingress()
}

func managedProfileSpec(name, role string) IAMInstanceProfileSpec {
	return IAMInstanceProfileSpec{
		Account: "test", Path: "/apps/", InstanceProfileName: name, RoleName: role,
		Tags: map[string]string{"Name": name, "env": "test"},
	}
}

func TestGenericIAMInstanceProfileCoreLifecycle(t *testing.T) {
	api := newStatefulIAMInstanceProfileAPI()
	client := setupGenericIAMInstanceProfile(t, api)
	spec := managedProfileSpec("generic-profile", "generic-role")

	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[IAMInstanceProfileSpec, IAMInstanceProfileOutputs]{
		Client: client, ServiceName: ServiceName, Key: spec.InstanceProfileName, Spec: spec, Snapshot: api.snapshot,
	})
	assert.Equal(t, []roleCall{{ProfileName: spec.InstanceProfileName, RoleName: spec.RoleName}}, api.removeRoleCalls,
		"Delete must remove the role association explicitly owned by roleName")
}

func TestGenericIAMInstanceProfileObservedImportLifecycle(t *testing.T) {
	api := newStatefulIAMInstanceProfileAPI()
	spec := managedProfileSpec("existing-profile", "existing-role")
	api.setProfile(observedFromSpec(spec))
	client := setupGenericIAMInstanceProfile(t, api)

	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[IAMInstanceProfileOutputs]{
		Client: client, ServiceName: ServiceName, Key: spec.InstanceProfileName,
		Ref: types.ImportRef{ResourceID: spec.InstanceProfileName, Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericIAMInstanceProfileReconcileConvergesRoleAndTags(t *testing.T) {
	api := newStatefulIAMInstanceProfileAPI()
	client := setupGenericIAMInstanceProfile(t, api)
	spec := managedProfileSpec("drift-profile", "desired-role")
	_, err := ingress.Object[types.ProvisionRequest, IAMInstanceProfileOutputs](client, ServiceName, spec.InstanceProfileName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	api.forceRoleAndTagDrift(spec.InstanceProfileName)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, spec.InstanceProfileName, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assertProfileMatchesSpec(t, spec, api.profile(spec.InstanceProfileName))
}

func TestGenericIAMInstanceProfileRecoversCreateAfterRoleAssociationFailure(t *testing.T) {
	api := newStatefulIAMInstanceProfileAPI()
	api.failAddRoleOnce = true
	client := setupGenericIAMInstanceProfile(t, api)
	spec := managedProfileSpec("partial-create-profile", "eventual-role")

	_, err := ingress.Object[types.ProvisionRequest, IAMInstanceProfileOutputs](client, ServiceName, spec.InstanceProfileName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.Equal(t, "", api.profile(spec.InstanceProfileName).RoleName)
	assert.Equal(t, types.StatusError, getProfileStatus(t, client, spec.InstanceProfileName).Status)

	_, err = ingress.Object[types.ProvisionRequest, IAMInstanceProfileOutputs](client, ServiceName, spec.InstanceProfileName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, 1, api.snapshot().Creates, "recovery must finish the existing profile, not create another")
	assertProfileMatchesSpec(t, spec, api.profile(spec.InstanceProfileName))
}

func TestGenericIAMInstanceProfileRetriesTransientRoleAssociation(t *testing.T) {
	api := newStatefulIAMInstanceProfileAPI()
	api.failAddRoleTransientOnce = true
	client := setupGenericIAMInstanceProfile(t, api)
	spec := managedProfileSpec("retry-profile", "retry-role")

	_, err := ingress.Object[types.ProvisionRequest, IAMInstanceProfileOutputs](client, ServiceName, spec.InstanceProfileName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.Len(t, api.addRoleCalls, 2, "the journaled callback should retry only the transient provider operation")
	assertProfileMatchesSpec(t, spec, api.profile(spec.InstanceProfileName))
}

func TestGenericIAMInstanceProfileRecoversInterruptedRoleReplacement(t *testing.T) {
	api := newStatefulIAMInstanceProfileAPI()
	client := setupGenericIAMInstanceProfile(t, api)
	spec := managedProfileSpec("role-change-profile", "role-a")
	_, err := ingress.Object[types.ProvisionRequest, IAMInstanceProfileOutputs](client, ServiceName, spec.InstanceProfileName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)

	api.mu.Lock()
	api.failAddRoleOnce = true
	api.mu.Unlock()
	spec.RoleName = "role-b"
	_, err = ingress.Object[types.ProvisionRequest, IAMInstanceProfileOutputs](client, ServiceName, spec.InstanceProfileName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Equal(t, "", api.profile(spec.InstanceProfileName).RoleName, "the durable remove step completed before the injected add failure")

	_, err = ingress.Object[types.ProvisionRequest, IAMInstanceProfileOutputs](client, ServiceName, spec.InstanceProfileName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.Equal(t, "role-b", api.profile(spec.InstanceProfileName).RoleName)
}

func TestGenericIAMInstanceProfileRejectsImmutableChanges(t *testing.T) {
	api := newStatefulIAMInstanceProfileAPI()
	client := setupGenericIAMInstanceProfile(t, api)
	spec := managedProfileSpec("immutable-profile", "role")
	_, err := ingress.Object[types.ProvisionRequest, IAMInstanceProfileOutputs](client, ServiceName, spec.InstanceProfileName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)

	changedPath := spec
	changedPath.Path = "/other/"
	_, err = ingress.Object[types.ProvisionRequest, IAMInstanceProfileOutputs](client, ServiceName, spec.InstanceProfileName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, changedPath))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path is immutable")

	changedName := spec
	changedName.InstanceProfileName = "different-profile"
	_, err = ingress.Object[types.ProvisionRequest, IAMInstanceProfileOutputs](client, ServiceName, spec.InstanceProfileName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, changedName))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instanceProfileName is immutable")
}

func TestGenericIAMInstanceProfileDeleteSurfacesUnownedProviderConflict(t *testing.T) {
	api := newStatefulIAMInstanceProfileAPI()
	client := setupGenericIAMInstanceProfile(t, api)
	spec := managedProfileSpec("conflict-profile", "owned-role")
	_, err := ingress.Object[types.ProvisionRequest, IAMInstanceProfileOutputs](client, ServiceName, spec.InstanceProfileName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	api.mu.Lock()
	api.deleteConflict = true
	api.mu.Unlock()

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, spec.InstanceProfileName, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DeleteConflict")
	assert.Equal(t, 1, api.snapshot().Deletes, "classified conflict must not be retried or trigger extra cleanup")
	assert.Empty(t, api.profile(spec.InstanceProfileName).RoleName, "the explicitly owned role association may be removed")
	assert.Equal(t, types.StatusError, getProfileStatus(t, client, spec.InstanceProfileName).Status)
}

func TestSpecFromObservedRoundTrip(t *testing.T) {
	spec := managedProfileSpec("round-trip-profile", "round-trip-role")
	actual := specFromObserved(observedFromSpec(spec))
	actual.Account = spec.Account
	assert.Equal(t, spec, actual)
}

func TestDiffTags(t *testing.T) {
	add, remove := diffTags(
		map[string]string{"env": "prod", "team": "platform"},
		map[string]string{"env": "dev", "owner": "alice", "praxis:managed-key": "internal"},
	)
	require.Equal(t, map[string]string{"env": "prod", "team": "platform"}, add)
	assert.Equal(t, []string{"owner"}, remove)
}

func getProfileStatus(t *testing.T, client *ingress.Client, key string) types.StatusResponse {
	t.Helper()
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return status
}

func assertProfileMatchesSpec(t *testing.T, spec IAMInstanceProfileSpec, observed ObservedState) {
	t.Helper()
	assert.Equal(t, spec.InstanceProfileName, observed.InstanceProfileName)
	assert.Equal(t, spec.Path, observed.Path)
	assert.Equal(t, spec.RoleName, observed.RoleName)
	assert.Equal(t, spec.Tags, drivers.FilterPraxisTags(observed.Tags))
}

func observedFromSpec(spec IAMInstanceProfileSpec) ObservedState {
	return ObservedState{
		Arn:               "arn:aws:iam::123456789012:instance-profile" + spec.Path + spec.InstanceProfileName,
		InstanceProfileId: "AIPAEXAMPLE", InstanceProfileName: spec.InstanceProfileName,
		Path: spec.Path, RoleName: spec.RoleName, Tags: cloneTags(spec.Tags), CreateDate: "2026-07-17T00:00:00Z",
	}
}

func cloneObservedState(observed ObservedState) ObservedState {
	observed.Tags = cloneTags(observed.Tags)
	return observed
}

func cloneTags(tags map[string]string) map[string]string {
	if tags == nil {
		return nil
	}
	return maps.Clone(tags)
}
