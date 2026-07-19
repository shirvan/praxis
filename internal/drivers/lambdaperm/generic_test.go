package lambdaperm

import (
	"context"
	"encoding/json"
	"errors"
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

type statefulPermissionAPI struct {
	mu sync.Mutex

	permissions      map[string]ObservedState
	physicalCreates  int
	addAttempts      int
	reads            int
	deletes          int
	failResponseOnce bool
}

func newStatefulPermissionAPI() *statefulPermissionAPI {
	return &statefulPermissionAPI{permissions: map[string]ObservedState{}}
}

func permissionKey(functionName, statementID string) string {
	return functionName + "~" + statementID
}

func observedFromPermissionSpec(spec LambdaPermissionSpec) ObservedState {
	condition := map[string]map[string]string{}
	if spec.SourceArn != "" {
		condition["ArnLike"] = map[string]string{"AWS:SourceArn": spec.SourceArn}
	}
	if spec.SourceAccount != "" {
		condition["StringEquals"] = map[string]string{"AWS:SourceAccount": spec.SourceAccount}
	}
	if spec.EventSourceToken != "" {
		condition["StringEquals"] = map[string]string{"lambda:EventSourceToken": spec.EventSourceToken}
	}
	conditionJSON := ""
	if len(condition) > 0 {
		raw, _ := json.Marshal(condition)
		conditionJSON = string(raw)
	}
	return ObservedState{
		StatementId: spec.StatementId, FunctionName: spec.FunctionName,
		Action: spec.Action, Principal: spec.Principal, SourceArn: spec.SourceArn,
		SourceAccount: spec.SourceAccount, EventSourceToken: spec.EventSourceToken,
		Condition: conditionJSON,
	}
}

func (f *statefulPermissionAPI) AddPermission(_ context.Context, spec LambdaPermissionSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addAttempts++
	key := permissionKey(spec.FunctionName, spec.StatementId)
	if _, exists := f.permissions[key]; exists {
		return "", &smithy.GenericAPIError{Code: "ResourceConflictException", Message: "statement exists"}
	}
	f.physicalCreates++
	observed := observedFromPermissionSpec(spec)
	f.permissions[key] = observed
	statement := marshalObservedStatement(observed)
	if f.failResponseOnce {
		f.failResponseOnce = false
		return "", errors.New("request timeout after AddPermission response was lost")
	}
	return statement, nil
}

func (f *statefulPermissionAPI) RemovePermission(_ context.Context, functionName, statementID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := permissionKey(functionName, statementID)
	if _, exists := f.permissions[key]; !exists {
		return &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "statement missing"}
	}
	f.deletes++
	delete(f.permissions, key)
	return nil
}

func (f *statefulPermissionAPI) GetPolicy(_ context.Context, functionName string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.policyLocked(functionName)
}

func (f *statefulPermissionAPI) GetPermission(_ context.Context, functionName, statementID string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	observed, exists := f.permissions[permissionKey(functionName, statementID)]
	if !exists {
		return ObservedState{}, &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "statement missing"}
	}
	return observed, nil
}

func (f *statefulPermissionAPI) policyLocked(functionName string) (string, error) {
	statements := make([]policyStatement, 0)
	for key := range f.permissions {
		observed := f.permissions[key]
		if observed.FunctionName != functionName {
			continue
		}
		statement := policyStatement{Sid: observed.StatementId, Principal: observed.Principal, Action: observed.Action}
		if observed.Condition != "" {
			_ = json.Unmarshal([]byte(observed.Condition), &statement.Condition)
		}
		statements = append(statements, statement)
	}
	if len(statements) == 0 {
		return "", &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "policy missing"}
	}
	raw, err := json.Marshal(map[string]any{"Statement": statements})
	return string(raw), err
}

func (f *statefulPermissionAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{
		Creates: f.physicalCreates, Reads: f.reads, Updates: f.deletes, Deletes: f.deletes,
	}
}

func (f *statefulPermissionAPI) seed(spec LambdaPermissionSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.permissions[permissionKey(spec.FunctionName, spec.StatementId)] = observedFromPermissionSpec(applyDefaults(spec))
}

func (f *statefulPermissionAPI) replaceObserved(functionName, statementID string, mutate func(*ObservedState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := permissionKey(functionName, statementID)
	observed := f.permissions[key]
	mutate(&observed)
	f.permissions[key] = observed
}

func (f *statefulPermissionAPI) forceDelete(functionName, statementID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.permissions, permissionKey(functionName, statementID))
}

func (f *statefulPermissionAPI) clonePermissions() map[string]ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.permissions)
}

type permissionDriftSink struct{}

func (permissionDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }
func (permissionDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error {
	return nil
}

func setupGenericPermission(t *testing.T, api PermissionAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericLambdaPermissionDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) PermissionAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(permissionDriftSink{})).Ingress()
}

func managedPermissionSpec(statementID string) LambdaPermissionSpec {
	return LambdaPermissionSpec{
		Account: "test", Region: "us-east-1", FunctionName: "processor", StatementId: statementID,
		Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com",
		SourceArn: "arn:aws:s3:::events", SourceAccount: "123456789012",
	}
}

func TestGenericLambdaPermissionCoreLifecycle(t *testing.T) {
	api := newStatefulPermissionAPI()
	client := setupGenericPermission(t, api)
	spec := managedPermissionSpec("core-lifecycle")
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[LambdaPermissionSpec, LambdaPermissionOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~processor~core-lifecycle", Spec: spec,
		Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, stored LambdaPermissionSpec) {
			assert.Equal(t, spec.FunctionName, stored.FunctionName)
			assert.Equal(t, spec.StatementId, stored.StatementId)
			assert.Equal(t, "us-east-1", stored.Region)
		},
	})
}

func TestGenericLambdaPermissionObservedImportLifecycle(t *testing.T) {
	api := newStatefulPermissionAPI()
	spec := managedPermissionSpec("observed-import")
	api.seed(spec)
	client := setupGenericPermission(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[LambdaPermissionOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~processor~observed-import",
		Ref: types.ImportRef{ResourceID: "processor~observed-import", Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericLambdaPermissionRecoversAmbiguousCreateExactlyOnce(t *testing.T) {
	api := newStatefulPermissionAPI()
	api.failResponseOnce = true
	client := setupGenericPermission(t, api)
	spec := managedPermissionSpec("ambiguous")

	outputs := provisionPermission(t, client, "us-east-1~processor~ambiguous", spec)
	assert.Equal(t, spec.StatementId, outputs.StatementId)
	assert.Equal(t, 1, api.snapshot().Creates)
	api.mu.Lock()
	assert.Equal(t, 1, api.addAttempts, "callback replay must observe the exact completed statement before adding again")
	api.mu.Unlock()
}

func TestGenericLambdaPermissionRejectsDifferentSameIdentity(t *testing.T) {
	api := newStatefulPermissionAPI()
	existing := managedPermissionSpec("collision")
	existing.Principal = "events.amazonaws.com"
	api.seed(existing)
	client := setupGenericPermission(t, api)

	_, err := ingress.Object[types.ProvisionRequest, LambdaPermissionOutputs](client, ServiceName, "us-east-1~processor~collision", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedPermissionSpec("collision")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Equal(t, "events.amazonaws.com", api.clonePermissions()[permissionKey("processor", "collision")].Principal)
}

func TestGenericLambdaPermissionConvergesReplaceOnlyDrift(t *testing.T) {
	api := newStatefulPermissionAPI()
	client := setupGenericPermission(t, api)
	spec := managedPermissionSpec("drift")
	key := "us-east-1~processor~drift"
	provisionPermission(t, client, key, spec)
	api.replaceObserved(spec.FunctionName, spec.StatementId, func(observed *ObservedState) {
		observed.Principal = "events.amazonaws.com"
	})

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Equal(t, spec.Principal, api.clonePermissions()[permissionKey(spec.FunctionName, spec.StatementId)].Principal)
	assert.Equal(t, 2, api.snapshot().Creates)
}

func TestGenericLambdaPermissionRejectsImmutableIdentityChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*LambdaPermissionSpec)
		want   string
	}{
		{name: "function", mutate: func(spec *LambdaPermissionSpec) { spec.FunctionName = "other" }, want: "functionName is immutable"},
		{name: "statement", mutate: func(spec *LambdaPermissionSpec) { spec.StatementId = "other" }, want: "statementId is immutable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := newStatefulPermissionAPI()
			client := setupGenericPermission(t, api)
			spec := managedPermissionSpec("immutable-" + test.name)
			key := "us-east-1~processor~" + spec.StatementId
			provisionPermission(t, client, key, spec)
			test.mutate(&spec)
			_, err := ingress.Object[types.ProvisionRequest, LambdaPermissionOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
			assert.Len(t, api.clonePermissions(), 1)
		})
	}
}

func TestGenericLambdaPermissionExternalDeleteRequiresReplacementWithoutCreate(t *testing.T) {
	api := newStatefulPermissionAPI()
	client := setupGenericPermission(t, api)
	spec := managedPermissionSpec("external-delete")
	key := "us-east-1~processor~external-delete"
	provisionPermission(t, client, key, spec)
	api.forceDelete(spec.FunctionName, spec.StatementId)
	before := api.snapshot()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "deleted externally")
	assert.Equal(t, before.Creates, api.snapshot().Creates)
}

func TestPermissionValidateProvisionSpec(t *testing.T) {
	spec := applyDefaults(LambdaPermissionSpec{Region: "us-east-1", FunctionName: "processor", StatementId: "allow-s3", Principal: "s3.amazonaws.com"})
	require.NoError(t, validateProvisionSpec(spec))
	for _, mutate := range []func(*LambdaPermissionSpec){
		func(spec *LambdaPermissionSpec) { spec.Region = "" },
		func(spec *LambdaPermissionSpec) { spec.FunctionName = "" },
		func(spec *LambdaPermissionSpec) { spec.StatementId = "" },
		func(spec *LambdaPermissionSpec) { spec.Principal = "" },
	} {
		candidate := spec
		mutate(&candidate)
		assert.Error(t, validateProvisionSpec(candidate))
	}
}

func TestPermissionHelpers(t *testing.T) {
	assert.Equal(t, "lambda:InvokeFunction", applyDefaults(LambdaPermissionSpec{}).Action)
	assert.Equal(t, "lambda:GetFunction", applyDefaults(LambdaPermissionSpec{Action: "lambda:GetFunction"}).Action)
	fn, sid, err := splitImportResourceID("my-function~allow-s3")
	require.NoError(t, err)
	assert.Equal(t, "my-function", fn)
	assert.Equal(t, "allow-s3", sid)
	_, _, err = splitImportResourceID("no-separator")
	assert.Error(t, err)
}

func provisionPermission(t *testing.T, client *ingress.Client, key string, spec LambdaPermissionSpec) LambdaPermissionOutputs {
	t.Helper()
	outputs, err := ingress.Object[types.ProvisionRequest, LambdaPermissionOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	return outputs
}
