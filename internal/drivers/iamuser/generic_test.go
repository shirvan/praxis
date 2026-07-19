package iamuser

import (
	"context"
	"errors"
	"maps"
	"slices"
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

type statefulIAMUserAPI struct {
	mu sync.Mutex

	observed          ObservedState
	creates           int
	reads             int
	updates           int
	deletes           int
	failInlinePutOnce bool
	accessKeys        int
	loginProfile      bool
	mfaDevices        int
}

type iamUserDriftSink struct{}

func (iamUserDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }

func (iamUserDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

func (f *statefulIAMUserAPI) CreateUser(_ context.Context, spec IAMUserSpec) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.UserName != "" {
		return "", "", errors.New("EntityAlreadyExists: user already exists")
	}
	f.creates++
	f.observed = ObservedState{
		Arn:    "arn:aws:iam::123456789012:user" + spec.Path + spec.UserName,
		UserId: "AIDAEXAMPLE", UserName: spec.UserName, Path: spec.Path,
		PermissionsBoundary: spec.PermissionsBoundary,
		InlinePolicies:      map[string]string{}, ManagedPolicyArns: []string{}, Groups: []string{},
		Tags: cloneStringMap(spec.Tags), CreateDate: "2026-07-17T00:00:00Z",
	}
	return f.observed.Arn, f.observed.UserId, nil
}

func (f *statefulIAMUserAPI) DescribeUser(_ context.Context, name string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.observed.UserName == "" || f.observed.UserName != name {
		return ObservedState{}, errors.New("NoSuchEntity: user does not exist")
	}
	return cloneObserved(f.observed), nil
}

func (f *statefulIAMUserAPI) DeleteUser(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.UserName == "" {
		return errors.New("NoSuchEntity: user does not exist")
	}
	if f.accessKeys > 0 || f.loginProfile || f.mfaDevices > 0 || len(f.observed.Groups) > 0 ||
		len(f.observed.InlinePolicies) > 0 || len(f.observed.ManagedPolicyArns) > 0 || f.observed.PermissionsBoundary != "" {
		return errors.New("DeleteConflict: user still has attached resources or credentials")
	}
	f.deletes++
	f.observed = ObservedState{}
	return nil
}

func (f *statefulIAMUserAPI) UpdateUserPath(_ context.Context, _ string, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.Path = path
	f.observed.Arn = "arn:aws:iam::123456789012:user" + path + f.observed.UserName
	return nil
}

func (f *statefulIAMUserAPI) PutUserPermissionsBoundary(_ context.Context, _ string, policyARN string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.PermissionsBoundary = policyARN
	return nil
}

func (f *statefulIAMUserAPI) DeleteUserPermissionsBoundary(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	f.observed.PermissionsBoundary = ""
	return nil
}

func (f *statefulIAMUserAPI) PutInlinePolicy(_ context.Context, _ string, name, document string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failInlinePutOnce {
		f.failInlinePutOnce = false
		return errors.New("MalformedPolicyDocument: injected partial-completion fault")
	}
	f.updates++
	if f.observed.InlinePolicies == nil {
		f.observed.InlinePolicies = map[string]string{}
	}
	f.observed.InlinePolicies[name] = normalizePolicyDocument(document)
	return nil
}

func (f *statefulIAMUserAPI) DeleteInlinePolicy(_ context.Context, _ string, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	delete(f.observed.InlinePolicies, name)
	return nil
}

func (f *statefulIAMUserAPI) AttachManagedPolicy(_ context.Context, _ string, policyARN string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	if !slices.Contains(f.observed.ManagedPolicyArns, policyARN) {
		f.observed.ManagedPolicyArns = append(f.observed.ManagedPolicyArns, policyARN)
		slices.Sort(f.observed.ManagedPolicyArns)
	}
	return nil
}

func (f *statefulIAMUserAPI) DetachManagedPolicy(_ context.Context, _ string, policyARN string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	f.observed.ManagedPolicyArns = slices.DeleteFunc(f.observed.ManagedPolicyArns, func(value string) bool {
		return value == policyARN
	})
	return nil
}

func (f *statefulIAMUserAPI) AddUserToGroup(_ context.Context, _ string, groupName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	if !slices.Contains(f.observed.Groups, groupName) {
		f.observed.Groups = append(f.observed.Groups, groupName)
		slices.Sort(f.observed.Groups)
	}
	return nil
}

func (f *statefulIAMUserAPI) RemoveUserFromGroup(_ context.Context, _ string, groupName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	f.observed.Groups = slices.DeleteFunc(f.observed.Groups, func(value string) bool { return value == groupName })
	return nil
}

func (f *statefulIAMUserAPI) UpdateTags(_ context.Context, _ string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.Tags = cloneStringMap(drivers.FilterPraxisTags(tags))
	return nil
}

func (f *statefulIAMUserAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulIAMUserAPI) user() ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneObserved(f.observed)
}

func (f *statefulIAMUserAPI) forceCompositeDrift() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed.Path = "/wrong/"
	f.observed.PermissionsBoundary = "arn:aws:iam::123456789012:policy/wrong-boundary"
	f.observed.InlinePolicies = map[string]string{"app": `{}`, "stale": `{}`}
	f.observed.ManagedPolicyArns = []string{"arn:aws:iam::aws:policy/ReadOnlyAccess"}
	f.observed.Groups = []string{"external-group"}
	f.observed.Tags = map[string]string{"env": "wrong", "stale": "remove-me"}
}

func setupGenericIAMUser(t *testing.T, api IAMUserAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericIAMUserDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) IAMUserAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(iamUserDriftSink{})).Ingress()
}

func managedUserSpec(name string) IAMUserSpec {
	return IAMUserSpec{
		Account: "test", Path: "/apps/", UserName: name,
		PermissionsBoundary: "arn:aws:iam::123456789012:policy/boundary",
		InlinePolicies: map[string]string{
			"app": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`,
		},
		ManagedPolicyArns: []string{"arn:aws:iam::aws:policy/AmazonSQSReadOnlyAccess"},
		Groups:            []string{"developers"},
		Tags:              map[string]string{"env": "test", "owner": "praxis"},
	}
}

func TestGenericIAMUserCoreLifecycle(t *testing.T) {
	api := &statefulIAMUserAPI{}
	client := setupGenericIAMUser(t, api)
	spec := managedUserSpec("generic-user")
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[IAMUserSpec, IAMUserOutputs]{
		Client: client, ServiceName: ServiceName, Key: spec.UserName, Spec: spec, Snapshot: api.snapshot,
	})
}

func TestGenericIAMUserRejectsImmutableNameAndRetainsInputs(t *testing.T) {
	api := &statefulIAMUserAPI{}
	client := setupGenericIAMUser(t, api)
	key := "immutable-user"
	_, err := ingress.Object[IAMUserSpec, IAMUserOutputs](client, ServiceName, key, "Provision").Request(t.Context(), managedUserSpec(key))
	require.NoError(t, err)
	accepted, err := ingress.Object[restate.Void, IAMUserSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	changed := accepted
	changed.UserName = "different-user"
	_, err = ingress.Object[IAMUserSpec, IAMUserOutputs](client, ServiceName, key, "Provision").Request(t.Context(), changed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "userName is immutable")
	retained, err := ingress.Object[restate.Void, IAMUserSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, accepted, retained)
}

func TestGenericIAMUserObservedImportLifecycle(t *testing.T) {
	api := &statefulIAMUserAPI{observed: observedFromSpec(managedUserSpec("existing-user"))}
	client := setupGenericIAMUser(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[IAMUserOutputs]{
		Client: client, ServiceName: ServiceName, Key: "existing-user",
		Ref: types.ImportRef{ResourceID: "existing-user", Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericIAMUserReconcileConvergesEveryCompositeComponent(t *testing.T) {
	api := &statefulIAMUserAPI{}
	client := setupGenericIAMUser(t, api)
	spec := managedUserSpec("drift-user")
	_, err := ingress.Object[IAMUserSpec, IAMUserOutputs](client, ServiceName, spec.UserName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	beforeDrift := api.snapshot()
	api.forceCompositeDrift()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, spec.UserName, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	after := api.snapshot()
	assert.GreaterOrEqual(t, after.Updates-beforeDrift.Updates, 6, "path, boundary, inline, managed, group, and tags must each mutate")
	assert.GreaterOrEqual(t, after.Deletes-beforeDrift.Deletes, 3, "stale inline, managed policy, and group must each be removed")
	assertUserMatchesSpec(t, spec, api.user())
}

func TestGenericIAMUserRecoversPartialCreateWithoutSecondUser(t *testing.T) {
	api := &statefulIAMUserAPI{failInlinePutOnce: true}
	client := setupGenericIAMUser(t, api)
	spec := managedUserSpec("partial-user")

	_, err := ingress.Object[IAMUserSpec, IAMUserOutputs](client, ServiceName, spec.UserName, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.Equal(t, spec.UserName, api.user().UserName)
	assert.Equal(t, types.StatusError, getUserStatus(t, client, spec.UserName).Status)

	_, err = ingress.Object[IAMUserSpec, IAMUserOutputs](client, ServiceName, spec.UserName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, 1, api.snapshot().Creates, "recovery must observe and finish the existing user")
	assertUserMatchesSpec(t, spec, api.user())
}

func TestGenericIAMUserProvisionConvergesMutablePath(t *testing.T) {
	api := &statefulIAMUserAPI{}
	client := setupGenericIAMUser(t, api)
	spec := managedUserSpec("path-user")
	_, err := ingress.Object[IAMUserSpec, IAMUserOutputs](client, ServiceName, spec.UserName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	spec.Path = "/services/"
	_, err = ingress.Object[IAMUserSpec, IAMUserOutputs](client, ServiceName, spec.UserName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, "/services/", api.user().Path)
}

func TestGenericIAMUserDeleteDoesNotCleanExternalCredentials(t *testing.T) {
	api := &statefulIAMUserAPI{}
	client := setupGenericIAMUser(t, api)
	spec := managedUserSpec("credential-user")
	_, err := ingress.Object[IAMUserSpec, IAMUserOutputs](client, ServiceName, spec.UserName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	api.mu.Lock()
	api.accessKeys = 2
	api.loginProfile = true
	api.mfaDevices = 1
	api.mu.Unlock()

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, spec.UserName, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DeleteConflict")
	api.mu.Lock()
	assert.Equal(t, 2, api.accessKeys)
	assert.True(t, api.loginProfile)
	assert.Equal(t, 1, api.mfaDevices)
	api.accessKeys = 0
	api.loginProfile = false
	api.mfaDevices = 0
	api.mu.Unlock()
	assert.NotEmpty(t, api.user().UserName)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, spec.UserName, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusDeleted, getUserStatus(t, client, spec.UserName).Status)
}

func TestGenericIAMUserImportPreservesCompositeDesiredState(t *testing.T) {
	observed := observedFromSpec(managedUserSpec("import-user"))
	observed.Tags["praxis:managed-key"] = "internal"
	api := &statefulIAMUserAPI{observed: observed}
	client := setupGenericIAMUser(t, api)
	_, err := ingress.Object[types.ImportRef, IAMUserOutputs](client, ServiceName, observed.UserName, "Import").Request(t.Context(), types.ImportRef{
		ResourceID: observed.UserName, Account: "test",
	})
	require.NoError(t, err)
	inputs, err := ingress.Object[restate.Void, IAMUserSpec](client, ServiceName, observed.UserName, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, observed.Path, inputs.Path)
	assert.Equal(t, observed.PermissionsBoundary, inputs.PermissionsBoundary)
	assert.Equal(t, normalizePolicyMap(observed.InlinePolicies), normalizePolicyMap(inputs.InlinePolicies))
	assert.ElementsMatch(t, observed.ManagedPolicyArns, inputs.ManagedPolicyArns)
	assert.ElementsMatch(t, observed.Groups, inputs.Groups)
	assert.NotContains(t, inputs.Tags, "praxis:managed-key")
}

func getUserStatus(t *testing.T, client *ingress.Client, key string) types.StatusResponse {
	t.Helper()
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return status
}

func assertUserMatchesSpec(t *testing.T, spec IAMUserSpec, observed ObservedState) {
	t.Helper()
	assert.Equal(t, spec.Path, observed.Path)
	assert.Equal(t, spec.PermissionsBoundary, observed.PermissionsBoundary)
	assert.Equal(t, normalizePolicyMap(spec.InlinePolicies), normalizePolicyMap(observed.InlinePolicies))
	assert.ElementsMatch(t, spec.ManagedPolicyArns, observed.ManagedPolicyArns)
	assert.ElementsMatch(t, spec.Groups, observed.Groups)
	assert.Equal(t, drivers.FilterPraxisTags(spec.Tags), drivers.FilterPraxisTags(observed.Tags))
}

func observedFromSpec(spec IAMUserSpec) ObservedState {
	return ObservedState{
		Arn:    "arn:aws:iam::123456789012:user" + spec.Path + spec.UserName,
		UserId: "AIDAEXAMPLE", UserName: spec.UserName, Path: spec.Path,
		PermissionsBoundary: spec.PermissionsBoundary, InlinePolicies: normalizePolicyMap(spec.InlinePolicies),
		ManagedPolicyArns: slices.Clone(spec.ManagedPolicyArns), Groups: slices.Clone(spec.Groups),
		Tags: cloneStringMap(spec.Tags), CreateDate: "2026-07-17T00:00:00Z",
	}
}

func cloneObserved(input ObservedState) ObservedState {
	input.InlinePolicies = cloneStringMap(input.InlinePolicies)
	input.ManagedPolicyArns = slices.Clone(input.ManagedPolicyArns)
	input.Groups = slices.Clone(input.Groups)
	input.Tags = cloneStringMap(input.Tags)
	return input
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	maps.Copy(output, input)
	return output
}
