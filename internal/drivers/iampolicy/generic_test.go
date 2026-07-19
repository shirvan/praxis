package iampolicy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

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

type statefulIAMPolicyAPI struct {
	mu sync.Mutex

	observed              ObservedState
	versions              []PolicyVersionInfo
	versionDocuments      map[string]string
	externalPrincipals    []string
	nextVersion           int
	creates               int
	reads                 int
	updates               int
	deletes               int
	failVersionCreateOnce bool
}

type iamPolicyDriftSink struct{}

func (iamPolicyDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }

func (iamPolicyDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

func (f *statefulIAMPolicyAPI) CreatePolicy(_ context.Context, spec IAMPolicySpec) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.Arn != "" {
		return "", "", errors.New("EntityAlreadyExists: policy already exists")
	}
	f.creates++
	arn := "arn:aws:iam::123456789012:policy" + spec.Path + spec.PolicyName
	f.observed = ObservedState{
		Arn: arn, PolicyId: "ANPAEXAMPLE", PolicyName: spec.PolicyName,
		Path: spec.Path, Description: spec.Description,
		PolicyDocument:   normalizePolicyDocument(spec.PolicyDocument),
		DefaultVersionId: "v1", Tags: clonePolicyMap(spec.Tags),
		CreateDate: "2026-07-17T00:00:00Z", UpdateDate: "2026-07-17T00:00:00Z",
	}
	f.nextVersion = 2
	f.versions = []PolicyVersionInfo{{VersionID: "v1", IsDefaultVersion: true, CreateDate: versionTime(1)}}
	f.versionDocuments = map[string]string{"v1": f.observed.PolicyDocument}
	return arn, f.observed.PolicyId, nil
}

func (f *statefulIAMPolicyAPI) DescribePolicy(_ context.Context, arn string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.observed.Arn == "" || f.observed.Arn != arn {
		return ObservedState{}, errors.New("NoSuchEntity: policy does not exist")
	}
	return clonePolicyObserved(f.observed), nil
}

func (f *statefulIAMPolicyAPI) DescribePolicyByName(_ context.Context, name, path string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.observed.Arn == "" || f.observed.PolicyName != name || (path != "" && f.observed.Path != path) {
		return ObservedState{}, errors.New("NoSuchEntity: policy does not exist")
	}
	return clonePolicyObserved(f.observed), nil
}

func (f *statefulIAMPolicyAPI) DeletePolicy(_ context.Context, arn string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.Arn == "" || f.observed.Arn != arn {
		return errors.New("NoSuchEntity: policy does not exist")
	}
	if f.observed.AttachmentCount > 0 || len(f.versions) > 1 {
		return errors.New("DeleteConflict: policy still has dependencies")
	}
	f.deletes++
	f.observed = ObservedState{}
	f.versions = nil
	f.versionDocuments = nil
	return nil
}

func (f *statefulIAMPolicyAPI) CreatePolicyVersion(_ context.Context, arn, document string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.Arn == "" || f.observed.Arn != arn {
		return errors.New("NoSuchEntity: policy does not exist")
	}
	if f.failVersionCreateOnce {
		f.failVersionCreateOnce = false
		return errors.New("MalformedPolicyDocument: injected version creation fault")
	}
	if len(f.versions) >= 5 {
		return errors.New("LimitExceeded: policy has five versions")
	}
	for i := range f.versions {
		f.versions[i].IsDefaultVersion = false
	}
	id := fmt.Sprintf("v%d", f.nextVersion)
	f.nextVersion++
	normalized := normalizePolicyDocument(document)
	f.versions = append(f.versions, PolicyVersionInfo{VersionID: id, IsDefaultVersion: true, CreateDate: versionTime(f.nextVersion)})
	if f.versionDocuments == nil {
		f.versionDocuments = map[string]string{}
	}
	f.versionDocuments[id] = normalized
	f.observed.PolicyDocument = normalized
	f.observed.DefaultVersionId = id
	f.updates++
	return nil
}

func (f *statefulIAMPolicyAPI) GetPolicyDocument(_ context.Context, arn, versionID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.Arn == "" || f.observed.Arn != arn {
		return "", errors.New("NoSuchEntity: policy does not exist")
	}
	document, ok := f.versionDocuments[versionID]
	if !ok {
		return "", errors.New("NoSuchEntity: version does not exist")
	}
	return document, nil
}

func (f *statefulIAMPolicyAPI) ListPolicyVersions(_ context.Context, arn string) ([]PolicyVersionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.observed.Arn == "" || f.observed.Arn != arn {
		return nil, errors.New("NoSuchEntity: policy does not exist")
	}
	return slices.Clone(f.versions), nil
}

func (f *statefulIAMPolicyAPI) DeletePolicyVersion(_ context.Context, arn, versionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.Arn == "" || f.observed.Arn != arn {
		return errors.New("NoSuchEntity: policy does not exist")
	}
	index := slices.IndexFunc(f.versions, func(version PolicyVersionInfo) bool { return version.VersionID == versionID })
	if index < 0 {
		return errors.New("NoSuchEntity: version does not exist")
	}
	if f.versions[index].IsDefaultVersion {
		return errors.New("DeleteConflict: cannot delete default version")
	}
	f.versions = slices.Delete(f.versions, index, index+1)
	delete(f.versionDocuments, versionID)
	f.deletes++
	return nil
}

func (f *statefulIAMPolicyAPI) UpdateTags(_ context.Context, arn string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.Arn == "" || f.observed.Arn != arn {
		return errors.New("NoSuchEntity: policy does not exist")
	}
	f.observed.Tags = clonePolicyMap(drivers.FilterPraxisTags(tags))
	f.updates++
	return nil
}

func (f *statefulIAMPolicyAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulIAMPolicyAPI) policy() ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return clonePolicyObserved(f.observed)
}

func (f *statefulIAMPolicyAPI) policyVersions() []PolicyVersionInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.versions)
}

func (f *statefulIAMPolicyAPI) setAttachments(principals ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.externalPrincipals = slices.Clone(principals)
	f.observed.AttachmentCount = int32(len(principals))
}

func (f *statefulIAMPolicyAPI) forceExternalDefault(document string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.versions {
		f.versions[i].IsDefaultVersion = false
	}
	id := fmt.Sprintf("v%d", f.nextVersion)
	f.nextVersion++
	normalized := normalizePolicyDocument(document)
	f.versions = append(f.versions, PolicyVersionInfo{VersionID: id, IsDefaultVersion: true, CreateDate: versionTime(f.nextVersion)})
	f.versionDocuments[id] = normalized
	f.observed.PolicyDocument = normalized
	f.observed.DefaultVersionId = id
}

func setupGenericIAMPolicy(t *testing.T, api IAMPolicyAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericIAMPolicyDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) IAMPolicyAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(iamPolicyDriftSink{})).Ingress()
}

func managedPolicySpec(name string) IAMPolicySpec {
	return IAMPolicySpec{
		Account: "test", Path: "/apps/", PolicyName: name,
		PolicyDocument: policyDocument("Allow", "s3:GetObject"),
		Description:    "application policy",
		Tags:           map[string]string{"env": "test", "owner": "praxis"},
	}
}

func TestGenericIAMPolicyCoreLifecycle(t *testing.T) {
	api := &statefulIAMPolicyAPI{}
	client := setupGenericIAMPolicy(t, api)
	spec := managedPolicySpec("generic-policy")
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[IAMPolicySpec, IAMPolicyOutputs]{
		Client: client, ServiceName: ServiceName, Key: spec.PolicyName, Spec: spec, Snapshot: api.snapshot,
	})
}

func TestGenericIAMPolicyObservedImportLifecycle(t *testing.T) {
	spec := managedPolicySpec("existing-policy")
	api := newStatefulIAMPolicy(spec)
	client := setupGenericIAMPolicy(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[IAMPolicyOutputs]{
		Client: client, ServiceName: ServiceName, Key: spec.PolicyName,
		Ref: types.ImportRef{ResourceID: spec.PolicyName, Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericIAMPolicyCreatesNewDefaultAWSVersionAndConvergesTags(t *testing.T) {
	api := &statefulIAMPolicyAPI{}
	client := setupGenericIAMPolicy(t, api)
	spec := managedPolicySpec("versioned-policy")
	provisionPolicy(t, client, spec)

	spec.PolicyDocument = policyDocument("Deny", "s3:DeleteObject")
	spec.Tags = map[string]string{"env": "prod"}
	provisionPolicy(t, client, spec)

	observed := api.policy()
	assert.True(t, policyDocumentsEqual(spec.PolicyDocument, observed.PolicyDocument))
	assert.Equal(t, map[string]string{"env": "prod"}, observed.Tags)
	assert.Equal(t, "v2", observed.DefaultVersionId)
	versions := api.policyVersions()
	require.Len(t, versions, 2)
	assert.True(t, slices.ContainsFunc(versions, func(version PolicyVersionInfo) bool {
		return version.VersionID == "v2" && version.IsDefaultVersion
	}))
	assert.True(t, slices.ContainsFunc(versions, func(version PolicyVersionInfo) bool {
		return version.VersionID == "v1" && !version.IsDefaultVersion
	}))
}

func TestGenericIAMPolicyReconcileCorrectsExternallyChangedDefaultVersion(t *testing.T) {
	api := &statefulIAMPolicyAPI{}
	client := setupGenericIAMPolicy(t, api)
	spec := managedPolicySpec("reconcile-version-policy")
	provisionPolicy(t, client, spec)
	api.forceExternalDefault(policyDocument("Deny", "s3:DeleteObject"))

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, spec.PolicyName, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.True(t, policyDocumentsEqual(spec.PolicyDocument, api.policy().PolicyDocument))
	assert.Equal(t, "v3", api.policy().DefaultVersionId)
}

func TestGenericIAMPolicyRotatesOldestVersionAtAWSFiveVersionLimit(t *testing.T) {
	api := &statefulIAMPolicyAPI{}
	client := setupGenericIAMPolicy(t, api)
	spec := managedPolicySpec("rotation-policy")
	provisionPolicy(t, client, spec)
	for i := 2; i <= 5; i++ {
		spec.PolicyDocument = policyDocument("Allow", fmt.Sprintf("s3:Action%d", i))
		provisionPolicy(t, client, spec)
	}
	require.Len(t, api.policyVersions(), 5)

	before := api.snapshot()
	spec.PolicyDocument = policyDocument("Deny", "s3:NewestAction")
	provisionPolicy(t, client, spec)
	after := api.snapshot()

	versions := api.policyVersions()
	require.Len(t, versions, 5, "rotation must keep IAM's five-version limit")
	assert.False(t, slices.ContainsFunc(versions, func(version PolicyVersionInfo) bool { return version.VersionID == "v1" }))
	assert.Equal(t, before.Deletes+1, after.Deletes, "oldest non-default provider version must be removed")
	assert.True(t, policyDocumentsEqual(spec.PolicyDocument, api.policy().PolicyDocument))
}

func TestGenericIAMPolicyRecoversAfterVersionRotationPartiallyCompletes(t *testing.T) {
	api := &statefulIAMPolicyAPI{}
	client := setupGenericIAMPolicy(t, api)
	spec := managedPolicySpec("rotation-recovery-policy")
	provisionPolicy(t, client, spec)
	for i := 2; i <= 5; i++ {
		spec.PolicyDocument = policyDocument("Allow", fmt.Sprintf("s3:Action%d", i))
		provisionPolicy(t, client, spec)
	}
	api.mu.Lock()
	api.failVersionCreateOnce = true
	api.mu.Unlock()
	spec.PolicyDocument = policyDocument("Deny", "s3:RecoveredAction")

	_, err := ingress.Object[IAMPolicySpec, IAMPolicyOutputs](client, ServiceName, spec.PolicyName, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.Len(t, api.policyVersions(), 4, "the independently journaled limit cleanup completed before the injected create fault")
	assert.Equal(t, types.StatusError, policyStatus(t, client, spec.PolicyName).Status)

	provisionPolicy(t, client, spec)
	assert.Equal(t, 1, api.snapshot().Creates, "recovery must finish the existing policy, not create another")
	assert.Len(t, api.policyVersions(), 5)
	assert.True(t, policyDocumentsEqual(spec.PolicyDocument, api.policy().PolicyDocument))
}

func TestGenericIAMPolicyDeleteLeavesExternalPrincipalAttachmentsUntouched(t *testing.T) {
	api := &statefulIAMPolicyAPI{}
	client := setupGenericIAMPolicy(t, api)
	spec := managedPolicySpec("attached-policy")
	provisionPolicy(t, client, spec)
	api.setAttachments("role/app", "user/deployer")
	before := api.snapshot()

	_, err := ingress.Object[restate.Void, restate.Void](client, ServiceName, spec.PolicyName, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "external principal attachment")
	assert.Equal(t, before.Deletes, api.snapshot().Deletes)
	api.mu.Lock()
	assert.Equal(t, []string{"role/app", "user/deployer"}, api.externalPrincipals)
	api.mu.Unlock()
	assert.NotEmpty(t, api.policy().Arn)

	api.setAttachments()
	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, spec.PolicyName, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
}

func TestGenericIAMPolicyDeleteRemovesOwnedNonDefaultVersions(t *testing.T) {
	api := &statefulIAMPolicyAPI{}
	client := setupGenericIAMPolicy(t, api)
	spec := managedPolicySpec("delete-versions-policy")
	provisionPolicy(t, client, spec)
	spec.PolicyDocument = policyDocument("Deny", "s3:DeleteObject")
	provisionPolicy(t, client, spec)
	require.Len(t, api.policyVersions(), 2)
	before := api.snapshot()

	_, err := ingress.Object[restate.Void, restate.Void](client, ServiceName, spec.PolicyName, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, before.Deletes+2, api.snapshot().Deletes, "one non-default version and the policy must be deleted")
	assert.Empty(t, api.policy().Arn)
}

func TestGenericIAMPolicyRejectsImmutableChanges(t *testing.T) {
	api := &statefulIAMPolicyAPI{}
	client := setupGenericIAMPolicy(t, api)
	spec := managedPolicySpec("immutable-policy")
	provisionPolicy(t, client, spec)

	spec.Description = "different immutable description"
	_, err := ingress.Object[IAMPolicySpec, IAMPolicyOutputs](client, ServiceName, spec.PolicyName, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "description is immutable")
	assert.Equal(t, "application policy", api.policy().Description)
}

func provisionPolicy(t *testing.T, client *ingress.Client, spec IAMPolicySpec) IAMPolicyOutputs {
	t.Helper()
	outputs, err := ingress.Object[IAMPolicySpec, IAMPolicyOutputs](client, ServiceName, spec.PolicyName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	return outputs
}

func policyStatus(t *testing.T, client *ingress.Client, key string) types.StatusResponse {
	t.Helper()
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return status
}

func newStatefulIAMPolicy(spec IAMPolicySpec) *statefulIAMPolicyAPI {
	spec = applyDefaults(spec)
	api := &statefulIAMPolicyAPI{}
	_, _, err := api.CreatePolicy(context.Background(), spec)
	if err != nil {
		panic(err)
	}
	api.creates = 0
	return api
}

func clonePolicyObserved(input ObservedState) ObservedState {
	input.Tags = clonePolicyMap(input.Tags)
	return input
}

func clonePolicyMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	maps.Copy(output, input)
	return output
}

func versionTime(version int) time.Time {
	return time.Date(2026, time.July, 17, 0, version, 0, 0, time.UTC)
}

func policyDocument(effect, action string) string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":%q,"Action":%q,"Resource":"*"}]}`, effect, action)
}
