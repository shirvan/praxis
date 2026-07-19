package iamrole

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

type statefulIAMRoleAPI struct {
	mu sync.Mutex

	observed          ObservedState
	instanceProfiles  []string
	creates           int
	reads             int
	updates           int
	deletes           int
	failInlinePutOnce bool
}

type iamRoleDriftSink struct{}

func (iamRoleDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }

func (iamRoleDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

func (f *statefulIAMRoleAPI) CreateRole(_ context.Context, spec IAMRoleSpec) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.RoleName != "" {
		return "", "", errors.New("EntityAlreadyExists: role already exists")
	}
	f.creates++
	f.observed = ObservedState{
		Arn:    "arn:aws:iam::123456789012:role" + spec.Path + spec.RoleName,
		RoleId: "AROAEXAMPLE", RoleName: spec.RoleName, Path: spec.Path,
		AssumeRolePolicyDocument: normalizePolicyDocument(spec.AssumeRolePolicyDocument),
		Description:              spec.Description, MaxSessionDuration: spec.MaxSessionDuration,
		PermissionsBoundary: spec.PermissionsBoundary,
		InlinePolicies:      map[string]string{}, ManagedPolicyArns: []string{},
		Tags: cloneMap(spec.Tags), CreateDate: "2026-07-17T00:00:00Z",
	}
	return f.observed.Arn, f.observed.RoleId, nil
}

func (f *statefulIAMRoleAPI) DescribeRole(_ context.Context, name string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.observed.RoleName == "" || f.observed.RoleName != name {
		return ObservedState{}, errors.New("NoSuchEntity: role does not exist")
	}
	return cloneObserved(f.observed), nil
}

func (f *statefulIAMRoleAPI) FindByTags(context.Context, map[string]string) (string, error) {
	return "", nil
}

func (f *statefulIAMRoleAPI) DeleteRole(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.RoleName == "" {
		return errors.New("NoSuchEntity: role does not exist")
	}
	if len(f.instanceProfiles) > 0 || len(f.observed.InlinePolicies) > 0 || len(f.observed.ManagedPolicyArns) > 0 {
		return errors.New("DeleteConflict: role still has attached resources")
	}
	f.deletes++
	f.observed = ObservedState{}
	return nil
}

func (f *statefulIAMRoleAPI) UpdateAssumeRolePolicy(_ context.Context, _ string, document string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.AssumeRolePolicyDocument = normalizePolicyDocument(document)
	return nil
}

func (f *statefulIAMRoleAPI) UpdateRole(_ context.Context, _ string, description string, duration int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.Description = description
	f.observed.MaxSessionDuration = duration
	return nil
}

func (f *statefulIAMRoleAPI) PutPermissionsBoundary(_ context.Context, _ string, policyARN string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.PermissionsBoundary = policyARN
	return nil
}

func (f *statefulIAMRoleAPI) DeletePermissionsBoundary(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	f.observed.PermissionsBoundary = ""
	return nil
}

func (f *statefulIAMRoleAPI) PutInlinePolicy(_ context.Context, _ string, name, document string) error {
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

func (f *statefulIAMRoleAPI) DeleteInlinePolicy(_ context.Context, _ string, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	delete(f.observed.InlinePolicies, name)
	return nil
}

func (f *statefulIAMRoleAPI) AttachManagedPolicy(_ context.Context, _ string, policyARN string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	if !slices.Contains(f.observed.ManagedPolicyArns, policyARN) {
		f.observed.ManagedPolicyArns = append(f.observed.ManagedPolicyArns, policyARN)
		slices.Sort(f.observed.ManagedPolicyArns)
	}
	return nil
}

func (f *statefulIAMRoleAPI) DetachManagedPolicy(_ context.Context, _ string, policyARN string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	f.observed.ManagedPolicyArns = slices.DeleteFunc(f.observed.ManagedPolicyArns, func(value string) bool {
		return value == policyARN
	})
	return nil
}

func (f *statefulIAMRoleAPI) UpdateTags(_ context.Context, _ string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.Tags = cloneMap(drivers.FilterPraxisTags(tags))
	return nil
}

func (f *statefulIAMRoleAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulIAMRoleAPI) role() ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneObserved(f.observed)
}

func (f *statefulIAMRoleAPI) forceCompositeDrift() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed.AssumeRolePolicyDocument = `{}`
	f.observed.Description = "drifted"
	f.observed.MaxSessionDuration = 7200
	f.observed.PermissionsBoundary = "arn:aws:iam::123456789012:policy/wrong-boundary"
	f.observed.InlinePolicies = map[string]string{"app": `{}`, "stale": `{}`}
	f.observed.ManagedPolicyArns = []string{"arn:aws:iam::aws:policy/ReadOnlyAccess"}
	f.observed.Tags = map[string]string{"env": "wrong", "stale": "remove-me"}
}

func setupGenericIAMRole(t *testing.T, api IAMRoleAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericIAMRoleDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) IAMRoleAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(iamRoleDriftSink{})).Ingress()
}

func managedRoleSpec(name string) IAMRoleSpec {
	return IAMRoleSpec{
		Account: "test", Path: "/apps/", RoleName: name,
		AssumeRolePolicyDocument: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`,
		Description:              "application role", MaxSessionDuration: 3600,
		PermissionsBoundary: "arn:aws:iam::123456789012:policy/boundary",
		InlinePolicies: map[string]string{
			"app": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`,
		},
		ManagedPolicyArns: []string{"arn:aws:iam::aws:policy/AmazonSQSReadOnlyAccess"},
		Tags:              map[string]string{"env": "test", "owner": "praxis"},
	}
}

func TestGenericIAMRoleCoreLifecycle(t *testing.T) {
	api := &statefulIAMRoleAPI{}
	client := setupGenericIAMRole(t, api)
	spec := managedRoleSpec("generic-role")
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[IAMRoleSpec, IAMRoleOutputs]{
		Client: client, ServiceName: ServiceName, Key: "generic-role", Spec: spec, Snapshot: api.snapshot,
	})
}

func TestGenericIAMRoleObservedImportLifecycle(t *testing.T) {
	api := &statefulIAMRoleAPI{observed: observedFromSpec(managedRoleSpec("existing-role"))}
	client := setupGenericIAMRole(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[IAMRoleOutputs]{
		Client: client, ServiceName: ServiceName, Key: "existing-role",
		Ref: types.ImportRef{ResourceID: "existing-role", Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericIAMRoleReconcileConvergesEveryCompositeComponent(t *testing.T) {
	api := &statefulIAMRoleAPI{}
	client := setupGenericIAMRole(t, api)
	spec := managedRoleSpec("drift-role")

	_, err := ingress.Object[types.ProvisionRequest, IAMRoleOutputs](client, ServiceName, spec.RoleName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	beforeDrift := api.snapshot()
	api.forceCompositeDrift()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, spec.RoleName, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	after := api.snapshot()
	assert.GreaterOrEqual(t, after.Updates-beforeDrift.Updates, 6, "trust, settings, boundary, inline, managed, and tags must each mutate")
	assert.GreaterOrEqual(t, after.Deletes-beforeDrift.Deletes, 2, "stale inline and managed policies must each be removed")
	assertRoleMatchesSpec(t, spec, api.role())
}

func TestGenericIAMRoleRecoversPartialCreateWithoutSecondRole(t *testing.T) {
	api := &statefulIAMRoleAPI{failInlinePutOnce: true}
	client := setupGenericIAMRole(t, api)
	spec := managedRoleSpec("partial-role")

	_, err := ingress.Object[types.ProvisionRequest, IAMRoleOutputs](client, ServiceName, spec.RoleName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.Equal(t, spec.RoleName, api.role().RoleName, "role creation must survive a later composite-step failure")
	status := getRoleStatus(t, client, spec.RoleName)
	assert.Equal(t, types.StatusError, status.Status)

	_, err = ingress.Object[types.ProvisionRequest, IAMRoleOutputs](client, ServiceName, spec.RoleName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, 1, api.snapshot().Creates, "recovery must observe and finish the existing role")
	assertRoleMatchesSpec(t, spec, api.role())
}

func TestGenericIAMRoleDeleteDoesNotCleanExternalInstanceProfiles(t *testing.T) {
	api := &statefulIAMRoleAPI{}
	client := setupGenericIAMRole(t, api)
	spec := managedRoleSpec("attached-role")
	_, err := ingress.Object[types.ProvisionRequest, IAMRoleOutputs](client, ServiceName, spec.RoleName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	api.mu.Lock()
	api.instanceProfiles = []string{"external-profile"}
	api.mu.Unlock()

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, spec.RoleName, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	api.mu.Lock()
	assert.Equal(t, []string{"external-profile"}, api.instanceProfiles)
	api.instanceProfiles = nil
	api.mu.Unlock()
	assert.NotEmpty(t, api.role().RoleName)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, spec.RoleName, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusDeleted, getRoleStatus(t, client, spec.RoleName).Status)
}

func TestGenericIAMRoleRejectsImmutableIdentityAndRetainsInputs(t *testing.T) {
	api := &statefulIAMRoleAPI{}
	client := setupGenericIAMRole(t, api)
	spec := managedRoleSpec("immutable-role")
	_, err := ingress.Object[types.ProvisionRequest, IAMRoleOutputs](client, ServiceName, spec.RoleName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	accepted, err := ingress.Object[restate.Void, IAMRoleSpec](client, ServiceName, spec.RoleName, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	tests := []struct {
		field  string
		mutate func(*IAMRoleSpec)
	}{
		{field: "roleName", mutate: func(s *IAMRoleSpec) { s.RoleName = "different-role" }},
		{field: "path", mutate: func(s *IAMRoleSpec) { s.Path = "/other/" }},
	}
	for _, tt := range tests {
		changed := accepted
		tt.mutate(&changed)
		_, err = ingress.Object[types.ProvisionRequest, IAMRoleOutputs](client, ServiceName, spec.RoleName, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, changed))
		require.Error(t, err)
		assert.Contains(t, err.Error(), tt.field+" is immutable")
		retained, getErr := ingress.Object[restate.Void, IAMRoleSpec](client, ServiceName, spec.RoleName, "GetInputs").Request(t.Context(), restate.Void{})
		require.NoError(t, getErr)
		assert.Equal(t, accepted, retained)
	}
	assert.Equal(t, "/apps/", api.role().Path)
}

func getRoleStatus(t *testing.T, client *ingress.Client, key string) types.StatusResponse {
	t.Helper()
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return status
}

func assertRoleMatchesSpec(t *testing.T, spec IAMRoleSpec, observed ObservedState) {
	t.Helper()
	assert.Equal(t, spec.Path, observed.Path)
	assert.True(t, policyDocumentsEqual(spec.AssumeRolePolicyDocument, observed.AssumeRolePolicyDocument))
	assert.Equal(t, spec.Description, observed.Description)
	assert.Equal(t, spec.MaxSessionDuration, observed.MaxSessionDuration)
	assert.Equal(t, spec.PermissionsBoundary, observed.PermissionsBoundary)
	assert.Equal(t, normalizePolicyMap(spec.InlinePolicies), normalizePolicyMap(observed.InlinePolicies))
	assert.ElementsMatch(t, spec.ManagedPolicyArns, observed.ManagedPolicyArns)
	assert.Equal(t, drivers.FilterPraxisTags(spec.Tags), drivers.FilterPraxisTags(observed.Tags))
}

func observedFromSpec(spec IAMRoleSpec) ObservedState {
	return ObservedState{
		Arn:    "arn:aws:iam::123456789012:role" + spec.Path + spec.RoleName,
		RoleId: "AROAEXAMPLE", RoleName: spec.RoleName, Path: spec.Path,
		AssumeRolePolicyDocument: normalizePolicyDocument(spec.AssumeRolePolicyDocument),
		Description:              spec.Description, MaxSessionDuration: spec.MaxSessionDuration,
		PermissionsBoundary: spec.PermissionsBoundary, InlinePolicies: normalizePolicyMap(spec.InlinePolicies),
		ManagedPolicyArns: slices.Clone(spec.ManagedPolicyArns), Tags: cloneMap(spec.Tags),
	}
}

func cloneObserved(input ObservedState) ObservedState {
	input.InlinePolicies = cloneMap(input.InlinePolicies)
	input.ManagedPolicyArns = slices.Clone(input.ManagedPolicyArns)
	input.Tags = cloneMap(input.Tags)
	return input
}

func cloneMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	maps.Copy(output, input)
	return output
}
