package nacl

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/praxiscloud/praxis/pkg/types"
)

type ruleCall struct {
	NetworkAclID string
	Rule         NetworkACLRule
	Egress       bool
}

type deleteRuleCall struct {
	NetworkAclID string
	RuleNumber   int
	Egress       bool
}

type replaceAssociationCall struct {
	AssociationID string
	NetworkAclID  string
}

type fakeNetworkACLAPI struct {
	mu sync.Mutex

	nextID            string
	nextAssociation   int
	createCalls       int
	deleteCalls       int
	updateCalls       int
	createEntryCalls  []ruleCall
	replaceEntryCalls []ruleCall
	deleteEntryCalls  []deleteRuleCall
	replaceAssocCalls []replaceAssociationCall
	observed          map[string]ObservedState
	managedKeys       map[string]string

	createFunc             func(context.Context, NetworkACLSpec) (string, error)
	describeFunc           func(context.Context, string) (ObservedState, error)
	deleteFunc             func(context.Context, string) error
	createEntryFunc        func(context.Context, string, NetworkACLRule, bool) error
	deleteEntryFunc        func(context.Context, string, int, bool) error
	replaceEntryFunc       func(context.Context, string, NetworkACLRule, bool) error
	replaceAssociationFunc func(context.Context, string, string) (string, error)
	updateFunc             func(context.Context, string, map[string]string) error
	findFunc               func(context.Context, string) (string, error)
	findAssociationFunc    func(context.Context, string) (string, error)
	findDefaultFunc        func(context.Context, string) (string, error)
}

func newFakeNetworkACLAPI() *fakeNetworkACLAPI {
	return &fakeNetworkACLAPI{
		nextID:          "acl-123",
		nextAssociation: 1,
		observed:        map[string]ObservedState{},
		managedKeys:     map[string]string{},
	}
}

func (f *fakeNetworkACLAPI) CreateNetworkACL(ctx context.Context, spec NetworkACLSpec) (string, error) {
	if f.createFunc != nil {
		return f.createFunc(ctx, spec)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	id := f.nextID
	if id == "" {
		id = fmt.Sprintf("acl-%d", f.createCalls)
	}
	tags := map[string]string{"praxis:managed-key": spec.ManagedKey}
	for key, value := range spec.Tags {
		tags[key] = value
	}
	f.observed[id] = ObservedState{NetworkAclId: id, VpcId: spec.VpcId, Tags: tags}
	if spec.ManagedKey != "" {
		f.managedKeys[spec.ManagedKey] = id
	}
	return id, nil
}

func (f *fakeNetworkACLAPI) DescribeNetworkACL(ctx context.Context, networkAclId string) (ObservedState, error) {
	if f.describeFunc != nil {
		return f.describeFunc(ctx, networkAclId)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obs, ok := f.observed[networkAclId]
	if !ok {
		return ObservedState{}, &mockAPIError{code: "InvalidNetworkAclID.NotFound", message: "missing"}
	}
	return cloneObservedState(obs), nil
}

func (f *fakeNetworkACLAPI) DeleteNetworkACL(ctx context.Context, networkAclId string) error {
	if f.deleteFunc != nil {
		return f.deleteFunc(ctx, networkAclId)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	obs, ok := f.observed[networkAclId]
	if !ok {
		return &mockAPIError{code: "InvalidNetworkAclID.NotFound", message: "missing"}
	}
	if obs.IsDefault {
		return &mockAPIError{code: "Client.CannotDelete", message: "default network acl"}
	}
	if len(obs.Associations) > 0 {
		return &mockAPIError{code: "DependencyViolation", message: "still associated"}
	}
	delete(f.observed, networkAclId)
	for key, value := range f.managedKeys {
		if value == networkAclId {
			delete(f.managedKeys, key)
		}
	}
	return nil
}

func (f *fakeNetworkACLAPI) CreateEntry(ctx context.Context, networkAclId string, rule NetworkACLRule, egress bool) error {
	if f.createEntryFunc != nil {
		return f.createEntryFunc(ctx, networkAclId, rule, egress)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createEntryCalls = append(f.createEntryCalls, ruleCall{NetworkAclID: networkAclId, Rule: rule, Egress: egress})
	obs := f.observed[networkAclId]
	target := &obs.IngressRules
	if egress {
		target = &obs.EgressRules
	}
	for _, existing := range *target {
		if existing.RuleNumber == rule.RuleNumber {
			return &mockAPIError{code: "NetworkAclEntryAlreadyExists", message: "duplicate rule"}
		}
	}
	*target = append(*target, rule)
	sortRules(*target)
	f.observed[networkAclId] = obs
	return nil
}

func (f *fakeNetworkACLAPI) DeleteEntry(ctx context.Context, networkAclId string, ruleNumber int, egress bool) error {
	if f.deleteEntryFunc != nil {
		return f.deleteEntryFunc(ctx, networkAclId, ruleNumber, egress)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteEntryCalls = append(f.deleteEntryCalls, deleteRuleCall{NetworkAclID: networkAclId, RuleNumber: ruleNumber, Egress: egress})
	obs := f.observed[networkAclId]
	target := obs.IngressRules
	if egress {
		target = obs.EgressRules
	}
	filtered := make([]NetworkACLRule, 0, len(target))
	removed := false
	for _, rule := range target {
		if rule.RuleNumber == ruleNumber {
			removed = true
			continue
		}
		filtered = append(filtered, rule)
	}
	if !removed {
		return &mockAPIError{code: "InvalidNetworkAclEntry.NotFound", message: "missing rule"}
	}
	if egress {
		obs.EgressRules = filtered
	} else {
		obs.IngressRules = filtered
	}
	f.observed[networkAclId] = obs
	return nil
}

func (f *fakeNetworkACLAPI) ReplaceEntry(ctx context.Context, networkAclId string, rule NetworkACLRule, egress bool) error {
	if f.replaceEntryFunc != nil {
		return f.replaceEntryFunc(ctx, networkAclId, rule, egress)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replaceEntryCalls = append(f.replaceEntryCalls, ruleCall{NetworkAclID: networkAclId, Rule: rule, Egress: egress})
	obs := f.observed[networkAclId]
	target := obs.IngressRules
	if egress {
		target = obs.EgressRules
	}
	replaced := false
	for index, existing := range target {
		if existing.RuleNumber == rule.RuleNumber {
			target[index] = rule
			replaced = true
			break
		}
	}
	if !replaced {
		return &mockAPIError{code: "InvalidNetworkAclEntry.NotFound", message: "missing rule"}
	}
	sortRules(target)
	if egress {
		obs.EgressRules = target
	} else {
		obs.IngressRules = target
	}
	f.observed[networkAclId] = obs
	return nil
}

func (f *fakeNetworkACLAPI) ReplaceNetworkACLAssociation(ctx context.Context, associationId string, networkAclId string) (string, error) {
	if f.replaceAssociationFunc != nil {
		return f.replaceAssociationFunc(ctx, associationId, networkAclId)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replaceAssocCalls = append(f.replaceAssocCalls, replaceAssociationCall{AssociationID: associationId, NetworkAclID: networkAclId})
	var subnetID string
	for aclID, obs := range f.observed {
		for index, association := range obs.Associations {
			if association.AssociationId == associationId {
				subnetID = association.SubnetId
				obs.Associations = append(obs.Associations[:index], obs.Associations[index+1:]...)
				sortAssociations(obs.Associations)
				f.observed[aclID] = obs
				break
			}
		}
		if subnetID != "" {
			break
		}
	}
	if subnetID == "" {
		return "", fmt.Errorf("association %s not found", associationId)
	}
	newAssociationID := f.nextAssociationIDLocked()
	target := f.observed[networkAclId]
	target.Associations = append(target.Associations, NetworkACLAssociation{AssociationId: newAssociationID, SubnetId: subnetID})
	sortAssociations(target.Associations)
	f.observed[networkAclId] = target
	return newAssociationID, nil
}

func (f *fakeNetworkACLAPI) UpdateTags(ctx context.Context, networkAclId string, tags map[string]string) error {
	if f.updateFunc != nil {
		return f.updateFunc(ctx, networkAclId, tags)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	obs := f.observed[networkAclId]
	praxisTags := map[string]string{}
	for key, value := range obs.Tags {
		if len(key) >= 7 && key[:7] == "praxis:" {
			praxisTags[key] = value
		}
	}
	obs.Tags = map[string]string{}
	for key, value := range praxisTags {
		obs.Tags[key] = value
	}
	for key, value := range tags {
		obs.Tags[key] = value
	}
	f.observed[networkAclId] = obs
	return nil
}

func (f *fakeNetworkACLAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
	if f.findFunc != nil {
		return f.findFunc(ctx, managedKey)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.managedKeys[managedKey], nil
}

func (f *fakeNetworkACLAPI) FindAssociationIdForSubnet(ctx context.Context, subnetId string) (string, error) {
	if f.findAssociationFunc != nil {
		return f.findAssociationFunc(ctx, subnetId)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, obs := range f.observed {
		for _, association := range obs.Associations {
			if association.SubnetId == subnetId {
				return association.AssociationId, nil
			}
		}
	}
	return "", fmt.Errorf("no network ACL association found for subnet %s", subnetId)
}

func (f *fakeNetworkACLAPI) FindDefaultNetworkACL(ctx context.Context, vpcId string) (string, error) {
	if f.findDefaultFunc != nil {
		return f.findDefaultFunc(ctx, vpcId)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, obs := range f.observed {
		if obs.VpcId == vpcId && obs.IsDefault {
			return id, nil
		}
	}
	return "", fmt.Errorf("default network ACL not found for VPC %s", vpcId)
}

func (f *fakeNetworkACLAPI) seedDefaultACL(vpcID string, subnetIDs ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fmt.Sprintf("acl-default-%s", vpcID)
	associations := make([]NetworkACLAssociation, 0, len(subnetIDs))
	for _, subnetID := range subnetIDs {
		associations = append(associations, NetworkACLAssociation{AssociationId: f.nextAssociationIDLocked(), SubnetId: subnetID})
	}
	f.observed[id] = ObservedState{
		NetworkAclId: id,
		VpcId:        vpcID,
		IsDefault:    true,
		Associations: associations,
		Tags:         map[string]string{"Name": "default"},
	}
	return id
}

func (f *fakeNetworkACLAPI) nextAssociationIDLocked() string {
	id := fmt.Sprintf("aclassoc-%d", f.nextAssociation)
	f.nextAssociation++
	return id
}

func cloneObservedState(obs ObservedState) ObservedState {
	clone := obs
	clone.IngressRules = append([]NetworkACLRule(nil), obs.IngressRules...)
	clone.EgressRules = append([]NetworkACLRule(nil), obs.EgressRules...)
	clone.Associations = append([]NetworkACLAssociation(nil), obs.Associations...)
	if obs.Tags != nil {
		clone.Tags = make(map[string]string, len(obs.Tags))
		for key, value := range obs.Tags {
			clone.Tags[key] = value
		}
	}
	return clone
}

func setupNetworkACLDriver(t *testing.T, api NetworkACLAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")

	driver := NewNetworkACLDriverWithFactory(nil, func(cfg aws.Config) NetworkACLAPI {
		return api
	})
	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress()
}

func testNetworkACLSpec(key, vpcID string) NetworkACLSpec {
	return NetworkACLSpec{
		Account:    "test",
		Region:     "us-east-1",
		VpcId:      vpcID,
		ManagedKey: key,
		IngressRules: []NetworkACLRule{
			{RuleNumber: 100, Protocol: "tcp", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 80, ToPort: 80},
		},
		EgressRules: []NetworkACLRule{
			{RuleNumber: 100, Protocol: "-1", RuleAction: "allow", CidrBlock: "0.0.0.0/0"},
		},
		Tags: map[string]string{"Name": "public-nacl", "env": "dev"},
	}
}

func TestServiceName(t *testing.T) {
	drv := NewNetworkACLDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestProvision_CreatesNetworkACL(t *testing.T) {
	api := newFakeNetworkACLAPI()
	client := setupNetworkACLDriver(t, api)
	key := "vpc-123~public"

	outputs, err := ingress.Object[NetworkACLSpec, NetworkACLOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testNetworkACLSpec(key, "vpc-123"))
	require.NoError(t, err)
	assert.Equal(t, "acl-123", outputs.NetworkAclId)
	assert.Equal(t, "vpc-123", outputs.VpcId)
	assert.Len(t, outputs.IngressRules, 1)
	assert.Len(t, outputs.EgressRules, 1)
	assert.Equal(t, 1, api.createCalls)
	assert.Len(t, api.createEntryCalls, 2)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}

func TestProvision_MissingVpcIdFails(t *testing.T) {
	api := newFakeNetworkACLAPI()
	client := setupNetworkACLDriver(t, api)
	key := "vpc-123~public"

	_, err := ingress.Object[NetworkACLSpec, NetworkACLOutputs](client, ServiceName, key, "Provision").Request(t.Context(), NetworkACLSpec{Account: "test", Region: "us-east-1", ManagedKey: key})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vpcId is required")
}

func TestProvision_DuplicateRuleNumberFails(t *testing.T) {
	api := newFakeNetworkACLAPI()
	client := setupNetworkACLDriver(t, api)
	key := "vpc-123~public"
	spec := testNetworkACLSpec(key, "vpc-123")
	spec.IngressRules = []NetworkACLRule{
		{RuleNumber: 100, Protocol: "tcp", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 80, ToPort: 80},
		{RuleNumber: 100, Protocol: "tcp", RuleAction: "allow", CidrBlock: "10.0.0.0/8", FromPort: 443, ToPort: 443},
	}

	_, err := ingress.Object[NetworkACLSpec, NetworkACLOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate ruleNumber 100")
}

func TestProvision_ConflictFails(t *testing.T) {
	api := newFakeNetworkACLAPI()
	api.managedKeys["vpc-123~public"] = "acl-existing"
	client := setupNetworkACLDriver(t, api)

	_, err := ingress.Object[NetworkACLSpec, NetworkACLOutputs](client, ServiceName, "vpc-123~public", "Provision").Request(t.Context(), testNetworkACLSpec("vpc-123~public", "vpc-123"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already managed by Praxis")
	assert.Equal(t, 0, api.createCalls)
}

func TestProvision_AssociationAddedAndRemoved(t *testing.T) {
	api := newFakeNetworkACLAPI()
	api.seedDefaultACL("vpc-123", "subnet-a")
	client := setupNetworkACLDriver(t, api)
	key := "vpc-123~public"
	spec := testNetworkACLSpec(key, "vpc-123")
	spec.SubnetAssociations = []string{"subnet-a"}

	outputs, err := ingress.Object[NetworkACLSpec, NetworkACLOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Len(t, outputs.Associations, 1)
	assert.Equal(t, "subnet-a", outputs.Associations[0].SubnetId)

	spec.SubnetAssociations = nil
	outputs, err = ingress.Object[NetworkACLSpec, NetworkACLOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Empty(t, outputs.Associations)

	defaultACLID, err := api.FindDefaultNetworkACL(t.Context(), "vpc-123")
	require.NoError(t, err)
	assert.Len(t, api.observed[defaultACLID].Associations, 1)
	assert.Equal(t, "subnet-a", api.observed[defaultACLID].Associations[0].SubnetId)
}

func TestProvision_TagAndRuleUpdate(t *testing.T) {
	api := newFakeNetworkACLAPI()
	client := setupNetworkACLDriver(t, api)
	key := "vpc-123~public"
	spec := testNetworkACLSpec(key, "vpc-123")

	_, err := ingress.Object[NetworkACLSpec, NetworkACLOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	spec.Tags["env"] = "prod"
	spec.IngressRules = []NetworkACLRule{{RuleNumber: 100, Protocol: "tcp", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 443, ToPort: 443}}
	outputs, err := ingress.Object[NetworkACLSpec, NetworkACLOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, "prod", api.observed[outputs.NetworkAclId].Tags["env"])
	assert.Len(t, api.replaceEntryCalls, 1)
	assert.Equal(t, 443, outputs.IngressRules[0].FromPort)
}

func TestImport_ExistingNetworkACL(t *testing.T) {
	api := newFakeNetworkACLAPI()
	api.observed["acl-123"] = ObservedState{
		NetworkAclId: "acl-123",
		VpcId:        "vpc-123",
		IngressRules: []NetworkACLRule{{RuleNumber: 100, Protocol: "6", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 80, ToPort: 80}},
		Tags:         map[string]string{"Name": "public", "praxis:managed-key": "vpc-123~public"},
	}
	client := setupNetworkACLDriver(t, api)

	outputs, err := ingress.Object[types.ImportRef, NetworkACLOutputs](client, ServiceName, "us-east-1~acl-123", "Import").Request(t.Context(), types.ImportRef{ResourceID: "acl-123", Account: "test"})
	require.NoError(t, err)
	assert.Equal(t, "acl-123", outputs.NetworkAclId)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, "us-east-1~acl-123", "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestImport_DefaultNetworkACL(t *testing.T) {
	api := newFakeNetworkACLAPI()
	api.observed["acl-default-vpc-123"] = ObservedState{NetworkAclId: "acl-default-vpc-123", VpcId: "vpc-123", IsDefault: true}
	client := setupNetworkACLDriver(t, api)

	outputs, err := ingress.Object[types.ImportRef, NetworkACLOutputs](client, ServiceName, "us-east-1~acl-default-vpc-123", "Import").Request(t.Context(), types.ImportRef{ResourceID: "acl-default-vpc-123", Account: "test"})
	require.NoError(t, err)
	assert.True(t, outputs.IsDefault)
}

func TestDelete_DisassociatesAndDeletes(t *testing.T) {
	api := newFakeNetworkACLAPI()
	api.seedDefaultACL("vpc-123", "subnet-a")
	client := setupNetworkACLDriver(t, api)
	key := "vpc-123~public"
	spec := testNetworkACLSpec(key, "vpc-123")
	spec.SubnetAssociations = []string{"subnet-a"}

	_, err := ingress.Object[NetworkACLSpec, NetworkACLOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, 1, api.deleteCalls)
	_, ok := api.observed["acl-123"]
	assert.False(t, ok)
}

func TestDelete_DefaultNetworkACLBlocked(t *testing.T) {
	api := newFakeNetworkACLAPI()
	api.observed["acl-default-vpc-123"] = ObservedState{NetworkAclId: "acl-default-vpc-123", VpcId: "vpc-123", IsDefault: true}
	client := setupNetworkACLDriver(t, api)
	key := "us-east-1~acl-default-vpc-123"

	_, err := ingress.Object[types.ImportRef, NetworkACLOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "acl-default-vpc-123", Account: "test", Mode: types.ModeManaged})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "default network ACL")
}

func TestDelete_ObservedModeBlocked(t *testing.T) {
	api := newFakeNetworkACLAPI()
	api.observed["acl-123"] = ObservedState{NetworkAclId: "acl-123", VpcId: "vpc-123"}
	client := setupNetworkACLDriver(t, api)
	key := "us-east-1~acl-123"

	_, err := ingress.Object[types.ImportRef, NetworkACLOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "acl-123", Account: "test"})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Observed mode")
}

func TestReconcile_DetectsRuleDrift(t *testing.T) {
	api := newFakeNetworkACLAPI()
	client := setupNetworkACLDriver(t, api)
	key := "vpc-123~public"

	_, err := ingress.Object[NetworkACLSpec, NetworkACLOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testNetworkACLSpec(key, "vpc-123"))
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.observed["acl-123"]
	obs.IngressRules = append(obs.IngressRules, NetworkACLRule{RuleNumber: 200, Protocol: "6", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 22, ToPort: 22})
	api.observed["acl-123"] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Len(t, api.observed["acl-123"].IngressRules, 1)
}

func TestReconcile_DetectsAssociationDrift(t *testing.T) {
	api := newFakeNetworkACLAPI()
	api.seedDefaultACL("vpc-123", "subnet-a")
	client := setupNetworkACLDriver(t, api)
	key := "vpc-123~public"
	spec := testNetworkACLSpec(key, "vpc-123")
	spec.SubnetAssociations = []string{"subnet-a"}

	_, err := ingress.Object[NetworkACLSpec, NetworkACLOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	api.mu.Lock()
	custom := api.observed["acl-123"]
	associationID := custom.Associations[0].AssociationId
	api.mu.Unlock()
	_, err = api.ReplaceNetworkACLAssociation(t.Context(), associationID, fmt.Sprintf("acl-default-%s", "vpc-123"))
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Len(t, api.observed["acl-123"].Associations, 1)
	assert.Equal(t, "subnet-a", api.observed["acl-123"].Associations[0].SubnetId)
}

func TestReconcile_DetectsTagDrift(t *testing.T) {
	api := newFakeNetworkACLAPI()
	client := setupNetworkACLDriver(t, api)
	key := "vpc-123~public"

	_, err := ingress.Object[NetworkACLSpec, NetworkACLOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testNetworkACLSpec(key, "vpc-123"))
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.observed["acl-123"]
	obs.Tags["env"] = "prod"
	api.observed["acl-123"] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Equal(t, "dev", api.observed["acl-123"].Tags["env"])
}

func TestReconcile_ObservedModeReportsOnly(t *testing.T) {
	api := newFakeNetworkACLAPI()
	api.observed["acl-123"] = ObservedState{NetworkAclId: "acl-123", VpcId: "vpc-123", Tags: map[string]string{"env": "dev"}}
	client := setupNetworkACLDriver(t, api)
	key := "us-east-1~acl-123"

	_, err := ingress.Object[types.ImportRef, NetworkACLOutputs](client, ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: "acl-123", Account: "test"})
	require.NoError(t, err)

	api.mu.Lock()
	obs := api.observed["acl-123"]
	obs.Tags["env"] = "prod"
	api.observed["acl-123"] = obs
	api.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.False(t, result.Correcting)
	assert.Equal(t, "prod", api.observed["acl-123"].Tags["env"])
}

func TestGetOutputs_ReturnsOutputs(t *testing.T) {
	api := newFakeNetworkACLAPI()
	client := setupNetworkACLDriver(t, api)
	key := "vpc-123~public"

	provided, err := ingress.Object[NetworkACLSpec, NetworkACLOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testNetworkACLSpec(key, "vpc-123"))
	require.NoError(t, err)

	outputs, err := ingress.Object[restate.Void, NetworkACLOutputs](client, ServiceName, key, "GetOutputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, provided.NetworkAclId, outputs.NetworkAclId)
	assert.Equal(t, provided.VpcId, outputs.VpcId)
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		VpcId:        "vpc-123",
		IngressRules: []NetworkACLRule{{RuleNumber: 100, Protocol: "6", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 80, ToPort: 80}},
		Associations: []NetworkACLAssociation{{AssociationId: "aclassoc-1", SubnetId: "subnet-a"}},
		Tags:         map[string]string{"Name": "public", "praxis:managed-key": "vpc-123~public"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, "vpc-123", spec.VpcId)
	assert.Equal(t, []string{"subnet-a"}, spec.SubnetAssociations)
	assert.Equal(t, "public", spec.Tags["Name"])
	assert.NotContains(t, spec.Tags, "praxis:managed-key")
}

func TestDefaultNetworkACLImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultNetworkACLImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultNetworkACLImportMode(types.ModeManaged))
}
