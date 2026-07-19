package iamgroup

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
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	"github.com/shirvan/praxis/internal/eventing"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type statefulIAMGroupAPI struct {
	mu sync.Mutex

	observed          ObservedState
	members           []string
	creates           int
	reads             int
	updates           int
	deletes           int
	failInlinePutOnce bool
}

type iamGroupDriftSink struct{}

func (iamGroupDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }

func (iamGroupDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

func (f *statefulIAMGroupAPI) CreateGroup(_ context.Context, spec IAMGroupSpec) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.GroupName != "" {
		return "", "", errors.New("EntityAlreadyExists: group already exists")
	}
	f.creates++
	f.observed = ObservedState{
		Arn:     "arn:aws:iam::123456789012:group" + spec.Path + spec.GroupName,
		GroupId: "AGPAEXAMPLE", GroupName: spec.GroupName, Path: spec.Path,
		InlinePolicies: map[string]string{}, ManagedPolicyArns: []string{},
		CreateDate: "2026-07-17T00:00:00Z",
	}
	return f.observed.Arn, f.observed.GroupId, nil
}

func (f *statefulIAMGroupAPI) DescribeGroup(_ context.Context, name string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.observed.GroupName == "" || f.observed.GroupName != name {
		return ObservedState{}, errors.New("NoSuchEntity: group does not exist")
	}
	return cloneObserved(f.observed), nil
}

func (f *statefulIAMGroupAPI) DeleteGroup(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.GroupName == "" {
		return errors.New("NoSuchEntity: group does not exist")
	}
	if len(f.members) > 0 || len(f.observed.InlinePolicies) > 0 || len(f.observed.ManagedPolicyArns) > 0 {
		return errors.New("DeleteConflict: group still has attached resources")
	}
	f.deletes++
	f.observed = ObservedState{}
	return nil
}

func (f *statefulIAMGroupAPI) UpdateGroupPath(_ context.Context, _ string, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.Path = path
	f.observed.Arn = "arn:aws:iam::123456789012:group" + path + f.observed.GroupName
	return nil
}

func (f *statefulIAMGroupAPI) PutInlinePolicy(_ context.Context, _ string, name, document string) error {
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

func (f *statefulIAMGroupAPI) DeleteInlinePolicy(_ context.Context, _ string, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	delete(f.observed.InlinePolicies, name)
	return nil
}

func (f *statefulIAMGroupAPI) AttachManagedPolicy(_ context.Context, _ string, policyARN string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	if !slices.Contains(f.observed.ManagedPolicyArns, policyARN) {
		f.observed.ManagedPolicyArns = append(f.observed.ManagedPolicyArns, policyARN)
		slices.Sort(f.observed.ManagedPolicyArns)
	}
	return nil
}

func (f *statefulIAMGroupAPI) DetachManagedPolicy(_ context.Context, _ string, policyARN string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	f.observed.ManagedPolicyArns = slices.DeleteFunc(f.observed.ManagedPolicyArns, func(value string) bool {
		return value == policyARN
	})
	return nil
}

func (f *statefulIAMGroupAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulIAMGroupAPI) group() ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneObserved(f.observed)
}

func (f *statefulIAMGroupAPI) forceCompositeDrift() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed.Path = "/wrong/"
	f.observed.InlinePolicies = map[string]string{"app": `{}`, "stale": `{}`}
	f.observed.ManagedPolicyArns = []string{"arn:aws:iam::aws:policy/ReadOnlyAccess"}
}

func setupGenericIAMGroup(t *testing.T, api IAMGroupAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericIAMGroupDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) IAMGroupAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(iamGroupDriftSink{})).Ingress()
}

func managedGroupSpec(name string) IAMGroupSpec {
	return IAMGroupSpec{
		Account: "test", Path: "/apps/", GroupName: name,
		InlinePolicies: map[string]string{
			"app": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`,
		},
		ManagedPolicyArns: []string{"arn:aws:iam::aws:policy/AmazonSQSReadOnlyAccess"},
	}
}

func TestGenericIAMGroupCoreLifecycle(t *testing.T) {
	api := &statefulIAMGroupAPI{}
	client := setupGenericIAMGroup(t, api)
	spec := managedGroupSpec("generic-group")
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[IAMGroupSpec, IAMGroupOutputs]{
		Client: client, ServiceName: ServiceName, Key: spec.GroupName, Spec: spec, Snapshot: api.snapshot,
	})
}

func TestGenericIAMGroupRejectsImmutableNameAndRetainsInputs(t *testing.T) {
	api := &statefulIAMGroupAPI{}
	client := setupGenericIAMGroup(t, api)
	key := "immutable-group"
	_, err := ingress.Object[IAMGroupSpec, IAMGroupOutputs](client, ServiceName, key, "Provision").Request(t.Context(), managedGroupSpec(key))
	require.NoError(t, err)
	accepted, err := ingress.Object[restate.Void, IAMGroupSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	changed := accepted
	changed.GroupName = "different-group"
	_, err = ingress.Object[IAMGroupSpec, IAMGroupOutputs](client, ServiceName, key, "Provision").Request(t.Context(), changed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "groupName is immutable")
	retained, err := ingress.Object[restate.Void, IAMGroupSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, accepted, retained)
}

func TestGenericIAMGroupObservedImportLifecycle(t *testing.T) {
	spec := managedGroupSpec("existing-group")
	api := &statefulIAMGroupAPI{observed: observedFromSpec(spec)}
	client := setupGenericIAMGroup(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[IAMGroupOutputs]{
		Client: client, ServiceName: ServiceName, Key: spec.GroupName,
		Ref: types.ImportRef{ResourceID: spec.GroupName, Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericIAMGroupReconcileConvergesEveryCompositeComponent(t *testing.T) {
	api := &statefulIAMGroupAPI{}
	client := setupGenericIAMGroup(t, api)
	spec := managedGroupSpec("drift-group")

	_, err := ingress.Object[IAMGroupSpec, IAMGroupOutputs](client, ServiceName, spec.GroupName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	beforeDrift := api.snapshot()
	api.forceCompositeDrift()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, spec.GroupName, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	after := api.snapshot()
	assert.GreaterOrEqual(t, after.Updates-beforeDrift.Updates, 3, "path, inline, and managed policy drift must each mutate")
	assert.GreaterOrEqual(t, after.Deletes-beforeDrift.Deletes, 2, "stale inline and managed policies must each be removed")
	assertGroupMatchesSpec(t, spec, api.group())
}

func TestGenericIAMGroupRecoversPartialCreateWithoutSecondGroup(t *testing.T) {
	api := &statefulIAMGroupAPI{failInlinePutOnce: true}
	client := setupGenericIAMGroup(t, api)
	spec := managedGroupSpec("partial-group")

	_, err := ingress.Object[IAMGroupSpec, IAMGroupOutputs](client, ServiceName, spec.GroupName, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.Equal(t, spec.GroupName, api.group().GroupName, "group creation must survive a later composite-step failure")
	assert.Equal(t, types.StatusError, getGroupStatus(t, client, spec.GroupName).Status)

	_, err = ingress.Object[IAMGroupSpec, IAMGroupOutputs](client, ServiceName, spec.GroupName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, 1, api.snapshot().Creates, "recovery must observe and finish the existing group")
	assertGroupMatchesSpec(t, spec, api.group())
}

func TestGenericIAMGroupConvergesPathChange(t *testing.T) {
	api := &statefulIAMGroupAPI{}
	client := setupGenericIAMGroup(t, api)
	spec := managedGroupSpec("path-group")
	_, err := ingress.Object[IAMGroupSpec, IAMGroupOutputs](client, ServiceName, spec.GroupName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	spec.Path = "/other/"
	_, err = ingress.Object[IAMGroupSpec, IAMGroupOutputs](client, ServiceName, spec.GroupName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, "/other/", api.group().Path)
}

func TestGenericIAMGroupDeleteDoesNotRemoveExternalMemberships(t *testing.T) {
	api := &statefulIAMGroupAPI{}
	client := setupGenericIAMGroup(t, api)
	spec := managedGroupSpec("member-group")
	_, err := ingress.Object[IAMGroupSpec, IAMGroupOutputs](client, ServiceName, spec.GroupName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	api.mu.Lock()
	api.members = []string{"external-user"}
	api.mu.Unlock()

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, spec.GroupName, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	api.mu.Lock()
	assert.Equal(t, []string{"external-user"}, api.members)
	api.members = nil
	api.mu.Unlock()
	assert.NotEmpty(t, api.group().GroupName)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, spec.GroupName, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusDeleted, getGroupStatus(t, client, spec.GroupName).Status)
}

func TestSpecFromObservedRoundTrip(t *testing.T) {
	spec := managedGroupSpec("round-trip")
	actual := specFromObserved(observedFromSpec(spec))
	actual.Account = spec.Account
	spec.InlinePolicies = normalizePolicyMap(spec.InlinePolicies)
	assert.Equal(t, spec, actual)
}

func getGroupStatus(t *testing.T, client *ingress.Client, key string) types.StatusResponse {
	t.Helper()
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return status
}

func assertGroupMatchesSpec(t *testing.T, spec IAMGroupSpec, observed ObservedState) {
	t.Helper()
	assert.Equal(t, spec.Path, observed.Path)
	assert.Equal(t, normalizePolicyMap(spec.InlinePolicies), normalizePolicyMap(observed.InlinePolicies))
	assert.ElementsMatch(t, spec.ManagedPolicyArns, observed.ManagedPolicyArns)
}

func observedFromSpec(spec IAMGroupSpec) ObservedState {
	return ObservedState{
		Arn:     "arn:aws:iam::123456789012:group" + spec.Path + spec.GroupName,
		GroupId: "AGPAEXAMPLE", GroupName: spec.GroupName, Path: spec.Path,
		InlinePolicies:    normalizePolicyMap(spec.InlinePolicies),
		ManagedPolicyArns: slices.Clone(spec.ManagedPolicyArns), CreateDate: "2026-07-17T00:00:00Z",
	}
}

func cloneObserved(input ObservedState) ObservedState {
	if input.InlinePolicies != nil {
		input.InlinePolicies = maps.Clone(input.InlinePolicies)
	}
	input.ManagedPolicyArns = slices.Clone(input.ManagedPolicyArns)
	return input
}
