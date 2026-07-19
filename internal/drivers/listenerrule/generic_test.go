package listenerrule

import (
	"context"
	"errors"
	"maps"
	"strconv"
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

type statefulListenerRuleAPI struct {
	mu                               sync.Mutex
	rules                            map[string]ObservedState
	creates, reads, updates, deletes int
	failCreateOnce                   bool
}

func newStatefulListenerRuleAPI() *statefulListenerRuleAPI {
	return &statefulListenerRuleAPI{rules: map[string]ObservedState{}}
}

func (f *statefulListenerRuleAPI) CreateRule(_ context.Context, _ string, spec ListenerRuleSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, observed := range f.rules {
		if observed.ListenerArn == spec.ListenerArn && observed.Priority == spec.Priority {
			return "", &smithy.GenericAPIError{Code: "PriorityInUse", Message: "priority in use"}
		}
	}
	f.creates++
	arn := "arn:listener-rule/" + strconv.Itoa(spec.Priority)
	f.rules[arn] = observedListenerRule(arn, spec)
	if f.failCreateOnce {
		f.failCreateOnce = false
		return "", errors.New("timeout after create response lost")
	}
	return arn, nil
}

func (f *statefulListenerRuleAPI) DescribeRule(_ context.Context, arn string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	observed, ok := f.rules[arn]
	if !ok {
		return ObservedState{}, ruleNotFoundError()
	}
	return cloneListenerRule(observed), nil
}

func (f *statefulListenerRuleAPI) FindRuleByPriority(_ context.Context, listenerArn string, priority int) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	for _, observed := range f.rules {
		if observed.ListenerArn == listenerArn && observed.Priority == priority {
			return cloneListenerRule(observed), nil
		}
	}
	return ObservedState{}, ruleNotFoundError()
}

func (f *statefulListenerRuleAPI) ListRules(_ context.Context, listenerArn string) ([]ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	var result []ObservedState
	for _, observed := range f.rules {
		if observed.ListenerArn == listenerArn {
			result = append(result, cloneListenerRule(observed))
		}
	}
	return result, nil
}

func (f *statefulListenerRuleAPI) DeleteRule(_ context.Context, arn string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rules[arn]; !ok {
		return ruleNotFoundError()
	}
	f.deletes++
	delete(f.rules, arn)
	return nil
}

func (f *statefulListenerRuleAPI) ModifyRule(_ context.Context, arn string, conditions []RuleCondition, actions []RuleAction) error {
	return f.mutate(arn, func(observed *ObservedState) {
		observed.Conditions = append([]RuleCondition(nil), conditions...)
		observed.Actions = append([]RuleAction(nil), actions...)
	})
}

func (f *statefulListenerRuleAPI) SetRulePriorities(_ context.Context, arn string, priority int) error {
	return f.mutate(arn, func(observed *ObservedState) { observed.Priority = priority })
}

func (f *statefulListenerRuleAPI) UpdateTags(_ context.Context, arn string, tags map[string]string) error {
	return f.mutate(arn, func(observed *ObservedState) { observed.Tags = maps.Clone(tags) })
}

func (f *statefulListenerRuleAPI) mutate(arn string, mutate func(*ObservedState)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed, ok := f.rules[arn]
	if !ok {
		return ruleNotFoundError()
	}
	f.updates++
	mutate(&observed)
	f.rules[arn] = observed
	return nil
}

func (f *statefulListenerRuleAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulListenerRuleAPI) seed(observed ObservedState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules[observed.RuleArn] = cloneListenerRule(observed)
}

func (f *statefulListenerRuleAPI) remove(arn string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rules, arn)
}

func (f *statefulListenerRuleAPI) force(arn string, mutate func(*ObservedState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed := f.rules[arn]
	mutate(&observed)
	f.rules[arn] = observed
}

func (f *statefulListenerRuleAPI) get(arn string) ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneListenerRule(f.rules[arn])
}

func cloneListenerRule(observed ObservedState) ObservedState {
	observed.Conditions = append([]RuleCondition(nil), observed.Conditions...)
	observed.Actions = append([]RuleAction(nil), observed.Actions...)
	observed.Tags = maps.Clone(observed.Tags)
	return observed
}

func observedListenerRule(arn string, spec ListenerRuleSpec) ObservedState {
	return ObservedState{
		RuleArn:     arn,
		ListenerArn: spec.ListenerArn,
		Priority:    spec.Priority,
		Conditions:  append([]RuleCondition(nil), spec.Conditions...),
		Actions:     append([]RuleAction(nil), spec.Actions...),
		Tags:        listenerRuleManagedTags(spec.Tags, spec.ManagedKey),
	}
}

func ruleNotFoundError() error {
	return &smithy.GenericAPIError{Code: "RuleNotFound", Message: "not found"}
}

type listenerRuleSink struct{}

func (listenerRuleSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }
func (listenerRuleSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error {
	return nil
}

func setupListenerRule(t *testing.T, api ListenerRuleAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericListenerRuleDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) ListenerRuleAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(listenerRuleSink{})).Ingress()
}

func listenerRuleSpec(priority int) ListenerRuleSpec {
	return ListenerRuleSpec{
		Account:     "test",
		Region:      "us-east-1",
		ListenerArn: "arn:listener/web",
		Priority:    priority,
		Conditions:  []RuleCondition{{Field: "path-pattern", Values: []string{"/api/*"}}},
		Actions:     []RuleAction{{Type: "forward", TargetGroupArn: "arn:target-group/api"}},
		Tags:        map[string]string{"env": "test"},
	}
}

func TestGenericListenerRuleCore(t *testing.T) {
	api := newStatefulListenerRuleAPI()
	client := setupListenerRule(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[ListenerRuleSpec, ListenerRuleOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~web-api", Spec: listenerRuleSpec(10), Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs ListenerRuleSpec) {
			assert.Equal(t, "us-east-1~web-api", inputs.ManagedKey)
		},
	})
}

func TestGenericListenerRuleObservedImport(t *testing.T) {
	api := newStatefulListenerRuleAPI()
	spec := listenerRuleSpec(11)
	spec.ManagedKey = "another-object"
	api.seed(observedListenerRule("arn:listener-rule/import", spec))
	client := setupListenerRule(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[ListenerRuleOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~import", Ref: types.ImportRef{ResourceID: "arn:listener-rule/import", Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericListenerRuleAmbiguousCreateAndCollision(t *testing.T) {
	api := newStatefulListenerRuleAPI()
	api.failCreateOnce = true
	client := setupListenerRule(t, api)
	outputs := provisionListenerRule(t, client, "us-east-1~replay", listenerRuleSpec(12))
	assert.NotEmpty(t, outputs.RuleArn)
	assert.Equal(t, 1, api.snapshot().Creates)

	api = newStatefulListenerRuleAPI()
	spec := listenerRuleSpec(13)
	api.seed(observedListenerRule("arn:listener-rule/collision", spec))
	client = setupListenerRule(t, api)
	_, err := ingress.Object[types.ProvisionRequest, ListenerRuleOutputs](client, ServiceName, "us-east-1~collision", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exact Praxis ownership")
	assert.Equal(t, 0, api.snapshot().Creates)
}

func TestGenericListenerRuleDriftImmutableAndExternalDelete(t *testing.T) {
	api := newStatefulListenerRuleAPI()
	client := setupListenerRule(t, api)
	key := "us-east-1~drift"
	spec := listenerRuleSpec(14)
	outputs := provisionListenerRule(t, client, key, spec)
	api.force(outputs.RuleArn, func(observed *ObservedState) {
		observed.Priority = 15
		observed.Conditions = []RuleCondition{{Field: "host-header", Values: []string{"old.example.com"}}}
		observed.Actions = []RuleAction{{Type: "fixed-response", FixedResponseConfig: &FixedResponseConfig{StatusCode: "503"}}}
		observed.Tags = map[string]string{"env": "old", "praxis:managed-key": key}
	})
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	corrected := api.get(outputs.RuleArn)
	assert.Equal(t, spec.Priority, corrected.Priority)
	assert.True(t, conditionsEqual(spec.Conditions, corrected.Conditions))
	assert.True(t, actionsEqual(spec.Actions, corrected.Actions))
	assert.Equal(t, "test", corrected.Tags["env"])

	spec.ListenerArn = "arn:listener/other"
	_, err = ingress.Object[types.ProvisionRequest, ListenerRuleOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "immutable")

	api = newStatefulListenerRuleAPI()
	client = setupListenerRule(t, api)
	key = "us-east-1~gone"
	outputs = provisionListenerRule(t, client, key, listenerRuleSpec(16))
	api.remove(outputs.RuleArn)
	result, err = ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericListenerRuleRejectsDefaultRuleImport(t *testing.T) {
	api := newStatefulListenerRuleAPI()
	spec := listenerRuleSpec(17)
	observed := observedListenerRule("arn:listener-rule/default", spec)
	observed.IsDefault = true
	api.seed(observed)
	client := setupListenerRule(t, api)
	_, err := ingress.Object[types.ImportRef, ListenerRuleOutputs](client, ServiceName, "us-east-1~default", "Import").Request(t.Context(), types.ImportRef{ResourceID: observed.RuleArn, Account: "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "default rules")
}

func provisionListenerRule(t *testing.T, client *ingress.Client, key string, spec ListenerRuleSpec) ListenerRuleOutputs {
	t.Helper()
	outputs, err := ingress.Object[types.ProvisionRequest, ListenerRuleOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	return outputs
}
