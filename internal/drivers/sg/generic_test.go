package sg

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
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	"github.com/shirvan/praxis/internal/eventing"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type statefulSGAPI struct {
	mu sync.Mutex

	observed          ObservedState
	exists            bool
	nextID            int
	creates           int
	reads             int
	updates           int
	deletes           int
	deleteAttempts    int
	dependencyOnce    bool
	failAuthorizeOnce bool
	ambiguousCreate   bool
	managedKeyFindErr error
	mutationOrder     []string
}

func (f *statefulSGAPI) DescribeSecurityGroup(_ context.Context, groupID string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if !f.exists || f.observed.GroupId != groupID {
		return ObservedState{}, errors.New("InvalidGroup.NotFound: security group does not exist")
	}
	return cloneSGObserved(f.observed), nil
}

func (f *statefulSGAPI) FindSecurityGroup(_ context.Context, groupName, vpcID string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if !f.exists || f.observed.GroupName != groupName || f.observed.VpcId != vpcID {
		return ObservedState{}, errors.New("InvalidGroup.NotFound: security group does not exist")
	}
	return cloneSGObserved(f.observed), nil
}

func (f *statefulSGAPI) FindByManagedKey(_ context.Context, managedKey string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.managedKeyFindErr != nil {
		return "", f.managedKeyFindErr
	}
	if !f.exists || f.observed.Tags[managedKeyTag] != managedKey {
		return "", nil
	}
	return f.observed.GroupId, nil
}

func (f *statefulSGAPI) FindByTags(_ context.Context, tags map[string]string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists {
		return "", nil
	}
	for key, value := range tags {
		if f.observed.Tags[key] != value {
			return "", nil
		}
	}
	return f.observed.GroupId, nil
}

func (f *statefulSGAPI) CreateSecurityGroup(_ context.Context, spec SecurityGroupSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exists && f.observed.GroupName == spec.GroupName && f.observed.VpcId == spec.VpcId {
		return "", errors.New("InvalidGroup.Duplicate: group already exists")
	}
	f.nextID++
	f.creates++
	f.exists = true
	tags := maps.Clone(spec.Tags)
	if tags == nil {
		tags = map[string]string{}
	}
	tags[managedKeyTag] = spec.ManagedKey
	f.observed = ObservedState{
		GroupId: "sg-00000001", GroupName: spec.GroupName, Description: spec.Description,
		VpcId: spec.VpcId, OwnerId: "123456789012", Tags: tags,
		EgressRules: []NormalizedRule{{Direction: "egress", Protocol: "all", FromPort: 0, ToPort: 0, Target: "cidr:0.0.0.0/0"}},
	}
	if f.ambiguousCreate {
		f.ambiguousCreate = false
		return "", errors.New("RequestTimeout: create response was lost")
	}
	return f.observed.GroupId, nil
}

func (f *statefulSGAPI) DeleteSecurityGroup(_ context.Context, groupID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteAttempts++
	if f.dependencyOnce {
		f.dependencyOnce = false
		return errors.New("DependencyViolation: network interface is still attached")
	}
	if !f.exists || f.observed.GroupId != groupID {
		return errors.New("InvalidGroup.NotFound: security group does not exist")
	}
	f.deletes++
	f.exists = false
	f.observed = ObservedState{}
	return nil
}

func (f *statefulSGAPI) AuthorizeIngress(_ context.Context, groupID string, rules []NormalizedRule) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.requireGroup(groupID); err != nil {
		return err
	}
	if f.failAuthorizeOnce {
		f.failAuthorizeOnce = false
		return errors.New("InvalidPermission.Malformed: injected authorization failure")
	}
	f.updates++
	f.mutationOrder = append(f.mutationOrder, "add-ingress")
	f.observed.IngressRules = mergeRuleSet(f.observed.IngressRules, rules)
	return nil
}

func (f *statefulSGAPI) AuthorizeEgress(_ context.Context, groupID string, rules []NormalizedRule) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.requireGroup(groupID); err != nil {
		return err
	}
	f.updates++
	f.mutationOrder = append(f.mutationOrder, "add-egress")
	f.observed.EgressRules = mergeRuleSet(f.observed.EgressRules, rules)
	return nil
}

func (f *statefulSGAPI) RevokeIngress(_ context.Context, groupID string, rules []NormalizedRule) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.requireGroup(groupID); err != nil {
		return err
	}
	f.updates++
	f.mutationOrder = append(f.mutationOrder, "remove-ingress")
	f.observed.IngressRules = subtractRuleSet(f.observed.IngressRules, rules)
	return nil
}

func (f *statefulSGAPI) RevokeEgress(_ context.Context, groupID string, rules []NormalizedRule) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.requireGroup(groupID); err != nil {
		return err
	}
	f.updates++
	f.mutationOrder = append(f.mutationOrder, "remove-egress")
	f.observed.EgressRules = subtractRuleSet(f.observed.EgressRules, rules)
	return nil
}

func (f *statefulSGAPI) UpdateTags(_ context.Context, groupID string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.requireGroup(groupID); err != nil {
		return err
	}
	f.updates++
	f.mutationOrder = append(f.mutationOrder, "tags")
	managedKey := f.observed.Tags[managedKeyTag]
	f.observed.Tags = maps.Clone(tags)
	if f.observed.Tags == nil {
		f.observed.Tags = map[string]string{}
	}
	if managedKey != "" {
		f.observed.Tags[managedKeyTag] = managedKey
	}
	return nil
}

func (f *statefulSGAPI) requireGroup(groupID string) error {
	if !f.exists || f.observed.GroupId != groupID {
		return errors.New("InvalidGroup.NotFound: security group does not exist")
	}
	return nil
}

func (f *statefulSGAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulSGAPI) group() ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneSGObserved(f.observed)
}

func (f *statefulSGAPI) seed(spec SecurityGroupSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exists = true
	f.observed = observedFromSGSpec(spec)
}

func (f *statefulSGAPI) seedOwned(spec SecurityGroupSpec, managedKey string) {
	f.seed(spec)
	f.mu.Lock()
	defer f.mu.Unlock()
	if managedKey != "" {
		f.observed.Tags[managedKeyTag] = managedKey
	}
}

func (f *statefulSGAPI) forceRuleAndTagDrift() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed.IngressRules = []NormalizedRule{{Direction: "ingress", Protocol: "tcp", FromPort: 22, ToPort: 22, Target: "cidr:0.0.0.0/0"}}
	f.observed.Tags = map[string]string{
		"env": "stale", "rogue": "remove", managedKeyTag: f.observed.Tags[managedKeyTag],
	}
	f.mutationOrder = nil
}

func (f *statefulSGAPI) forceDeleteExternally() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exists = false
	f.observed = ObservedState{}
}

type sgDriftSink struct{}

func (sgDriftSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (sgDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

func setupGenericSG(t *testing.T, api SGAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericSecurityGroupDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) SGAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(sgDriftSink{})).Ingress()
}

func managedSGSpec() SecurityGroupSpec {
	return SecurityGroupSpec{
		Account: "test", GroupName: "web", Description: "web traffic", VpcId: "vpc-123",
		IngressRules: []IngressRule{{Protocol: "tcp", FromPort: 443, ToPort: 443, CidrBlock: "0.0.0.0/0"}},
		EgressRules:  []EgressRule{{Protocol: "-1", FromPort: 0, ToPort: 0, CidrBlock: "0.0.0.0/0"}},
		Tags:         map[string]string{"env": "test"},
	}
}

func TestGenericSGCoreLifecycle(t *testing.T) {
	api := &statefulSGAPI{}
	client := setupGenericSG(t, api)
	spec := managedSGSpec()
	key := "vpc-123~web"
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[SecurityGroupSpec, SecurityGroupOutputs]{
		Client: client, ServiceName: ServiceName, Key: key, Spec: spec, Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, stored SecurityGroupSpec) {
			expected := spec
			expected.ManagedKey = key
			assert.Equal(t, expected, stored)
		},
	})
}

func TestGenericSGObservedImportLifecycle(t *testing.T) {
	api := &statefulSGAPI{}
	spec := managedSGSpec()
	api.seed(spec)
	client := setupGenericSG(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[SecurityGroupOutputs]{
		Client: client, ServiceName: ServiceName, Key: "sg-00000001",
		Ref: types.ImportRef{ResourceID: "sg-00000001", Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericSGConvergesRulesAddBeforeRemoveAndTags(t *testing.T) {
	api := &statefulSGAPI{}
	client := setupGenericSG(t, api)
	spec := managedSGSpec()
	provisionSG(t, client, spec)
	api.forceRuleAndTagDrift()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, "vpc-123~web", "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.False(t, HasDrift(spec, api.group()))
	assert.Equal(t, "vpc-123~web", api.group().Tags[managedKeyTag])
	api.mu.Lock()
	assert.Equal(t, []string{"add-ingress", "remove-ingress", "tags"}, api.mutationOrder)
	api.mu.Unlock()
}

func TestGenericSGCreateAtomicallyAttachesManagedKey(t *testing.T) {
	api := &statefulSGAPI{}
	client := setupGenericSG(t, api)
	provisionSG(t, client, managedSGSpec())

	observed := api.group()
	assert.Equal(t, "vpc-123~web", observed.Tags[managedKeyTag])
	assert.Equal(t, "test", observed.Tags["env"])
}

func TestGenericSGRecoversAmbiguousCreateByManagedKey(t *testing.T) {
	api := &statefulSGAPI{ambiguousCreate: true}
	client := setupGenericSG(t, api)

	outputs := provisionSG(t, client, managedSGSpec())
	assert.Equal(t, "sg-00000001", outputs.GroupId)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.Equal(t, "vpc-123~web", api.group().Tags[managedKeyTag])
}

func TestGenericSGRecoversExistingExactManagedKeyWithoutCreate(t *testing.T) {
	api := &statefulSGAPI{}
	api.seedOwned(managedSGSpec(), "vpc-123~web")
	client := setupGenericSG(t, api)

	outputs := provisionSG(t, client, managedSGSpec())
	assert.Equal(t, "sg-00000001", outputs.GroupId)
	assert.Zero(t, api.snapshot().Creates)
}

func TestGenericSGRejectsSameNameDifferentOwnership(t *testing.T) {
	for _, test := range []struct {
		name       string
		managedKey string
	}{
		{name: "unmanaged external resource"},
		{name: "different Praxis owner", managedKey: "vpc-123~other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := &statefulSGAPI{}
			api.seedOwned(managedSGSpec(), test.managedKey)
			client := setupGenericSG(t, api)

			_, err := ingress.Object[SecurityGroupSpec, SecurityGroupOutputs](client, ServiceName, "vpc-123~web", "Provision").Request(t.Context(), managedSGSpec())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "different ownership")
			assert.Zero(t, api.snapshot().Creates)
			assert.Equal(t, "sg-00000001", api.group().GroupId)
		})
	}
}

func TestGenericSGRejectsManagedKeyCollisionWithDifferentIdentity(t *testing.T) {
	api := &statefulSGAPI{}
	conflict := managedSGSpec()
	conflict.GroupName = "database"
	api.seedOwned(conflict, "vpc-123~web")
	client := setupGenericSG(t, api)

	_, err := ingress.Object[SecurityGroupSpec, SecurityGroupOutputs](client, ServiceName, "vpc-123~web", "Provision").Request(t.Context(), managedSGSpec())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incompatible resource")
	assert.Contains(t, err.Error(), "groupName is immutable")
	assert.Zero(t, api.snapshot().Creates)
}

func TestGenericSGRejectsAmbiguousManagedKeyOwnership(t *testing.T) {
	api := &statefulSGAPI{managedKeyFindErr: &ownershipConflictError{message: "ownership corruption: two security groups claim the managed key"}}
	client := setupGenericSG(t, api)

	_, err := ingress.Object[SecurityGroupSpec, SecurityGroupOutputs](client, ServiceName, "vpc-123~web", "Provision").Request(t.Context(), managedSGSpec())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ownership corruption")
	assert.Zero(t, api.snapshot().Creates)
}

func TestGenericSGRejectsImmutableIdentityChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*SecurityGroupSpec)
		field  string
	}{
		{name: "group name", field: "groupName is immutable", mutate: func(spec *SecurityGroupSpec) { spec.GroupName = "api" }},
		{name: "description", field: "description is immutable", mutate: func(spec *SecurityGroupSpec) { spec.Description = "changed" }},
		{name: "VPC", field: "vpcId is immutable", mutate: func(spec *SecurityGroupSpec) { spec.VpcId = "vpc-456" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := &statefulSGAPI{}
			client := setupGenericSG(t, api)
			spec := managedSGSpec()
			provisionSG(t, client, spec)
			before := api.group()
			test.mutate(&spec)

			_, err := ingress.Object[SecurityGroupSpec, SecurityGroupOutputs](client, ServiceName, "vpc-123~web", "Provision").Request(t.Context(), spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.field)
			assert.Contains(t, err.Error(), "delete and reprovision")
			assert.Equal(t, before, api.group())
			assert.Equal(t, 1, api.snapshot().Creates)
		})
	}
}

func TestGenericSGRecoversPartialRuleConvergenceOnProvision(t *testing.T) {
	api := &statefulSGAPI{}
	client := setupGenericSG(t, api)
	spec := managedSGSpec()
	provisionSG(t, client, spec)
	spec.IngressRules = append(spec.IngressRules, IngressRule{
		Protocol: "tcp", FromPort: 8443, ToPort: 8443, CidrBlock: "10.0.0.0/8",
	})
	api.failAuthorizeOnce = true

	_, err := ingress.Object[SecurityGroupSpec, SecurityGroupOutputs](client, ServiceName, "vpc-123~web", "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Equal(t, types.StatusError, getSGStatus(t, client).Status)
	assert.Equal(t, 1, api.snapshot().Creates)

	provisionSG(t, client, spec)
	assert.False(t, HasDrift(spec, api.group()))
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericSGDeleteRetriesDependencyViolation(t *testing.T) {
	api := &statefulSGAPI{dependencyOnce: true}
	client := setupGenericSG(t, api)
	provisionSG(t, client, managedSGSpec())

	_, err := ingress.Object[restate.Void, restate.Void](client, ServiceName, "vpc-123~web", "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	api.mu.Lock()
	assert.Equal(t, 2, api.deleteAttempts)
	api.mu.Unlock()
}

func TestGenericSGExternalDeleteRequiresReplacementWithoutCreation(t *testing.T) {
	api := &statefulSGAPI{}
	client := setupGenericSG(t, api)
	provisionSG(t, client, managedSGSpec())
	api.forceDeleteExternally()
	before := api.snapshot()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, "vpc-123~web", "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "deleted externally")
	assert.Equal(t, before.Creates, api.snapshot().Creates)
	assert.Equal(t, types.StatusError, getSGStatus(t, client).Status)
}

func TestSpecFromObservedPreservesRulesAndFiltersPraxisTags(t *testing.T) {
	observed := observedFromSGSpec(managedSGSpec())
	observed.Tags["praxis:managed-key"] = "vpc-123~web"
	spec := specFromObserved(observed)
	assert.Equal(t, managedSGSpec().GroupName, spec.GroupName)
	assert.Equal(t, managedSGSpec().IngressRules, spec.IngressRules)
	assert.Equal(t, managedSGSpec().EgressRules, spec.EgressRules)
	assert.Equal(t, map[string]string{"env": "test"}, spec.Tags)
}

func provisionSG(t *testing.T, client *ingress.Client, spec SecurityGroupSpec) SecurityGroupOutputs {
	t.Helper()
	outputs, err := ingress.Object[SecurityGroupSpec, SecurityGroupOutputs](client, ServiceName, "vpc-123~web", "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	return outputs
}

func getSGStatus(t *testing.T, client *ingress.Client) types.StatusResponse {
	t.Helper()
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, "vpc-123~web", "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return status
}

func observedFromSGSpec(spec SecurityGroupSpec) ObservedState {
	ingress, egress := SplitByDirection(Normalize(spec))
	return ObservedState{
		Region: "us-east-1", GroupId: "sg-00000001", GroupName: spec.GroupName,
		Description: spec.Description, VpcId: spec.VpcId, OwnerId: "123456789012",
		IngressRules: ingress, EgressRules: egress, Tags: maps.Clone(spec.Tags),
	}
}

func cloneSGObserved(observed ObservedState) ObservedState {
	observed.IngressRules = append([]NormalizedRule(nil), observed.IngressRules...)
	observed.EgressRules = append([]NormalizedRule(nil), observed.EgressRules...)
	observed.Tags = maps.Clone(observed.Tags)
	return observed
}

func mergeRuleSet(current, additions []NormalizedRule) []NormalizedRule {
	set := make(map[string]NormalizedRule, len(current)+len(additions))
	for _, rule := range current {
		set[rule.ruleKey()] = rule
	}
	for _, rule := range additions {
		set[rule.ruleKey()] = rule
	}
	result := make([]NormalizedRule, 0, len(set))
	for _, rule := range set {
		result = append(result, rule)
	}
	sortRules(result)
	return result
}

func subtractRuleSet(current, removals []NormalizedRule) []NormalizedRule {
	remove := make(map[string]struct{}, len(removals))
	for _, rule := range removals {
		remove[rule.ruleKey()] = struct{}{}
	}
	result := make([]NormalizedRule, 0, len(current))
	for _, rule := range current {
		if _, found := remove[rule.ruleKey()]; !found {
			result = append(result, rule)
		}
	}
	return result
}
