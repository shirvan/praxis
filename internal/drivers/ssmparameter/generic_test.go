package ssmparameter

import (
	"context"
	"encoding/json"
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

type statefulParameterAPI struct {
	mu sync.Mutex

	observed            ObservedState
	exists              bool
	physicalCreates     int
	putAttempts         int
	reads               int
	updates             int
	deletes             int
	failPutResponseOnce bool
}

func (f *statefulParameterAPI) PutParameter(_ context.Context, spec SSMParameterSpec, overwrite bool) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putAttempts++
	if f.exists && !overwrite {
		return 0, errors.New("ParameterAlreadyExists: parameter already exists")
	}
	if !f.exists {
		f.physicalCreates++
		f.observed.Version = 0
	} else {
		f.updates++
	}
	f.exists = true
	f.observed.Version++
	f.observed.ARN = "arn:aws:ssm:us-east-1:123456789012:parameter" + spec.ParameterName
	f.observed.ParameterName = spec.ParameterName
	f.observed.Type = spec.Type
	f.observed.Value = spec.Value
	f.observed.Description = spec.Description
	f.observed.Tier = spec.Tier
	f.observed.KmsKeyID = spec.KmsKeyID
	if spec.Type == "SecureString" && spec.KmsKeyID == "" {
		f.observed.KmsKeyID = "alias/aws/ssm"
	}
	f.observed.AllowedPattern = spec.AllowedPattern
	f.observed.DataType = spec.DataType
	if !overwrite {
		f.observed.Tags = managedTags(spec.Tags, spec.ManagedKey)
	}
	version := f.observed.Version
	if f.failPutResponseOnce {
		f.failPutResponseOnce = false
		return 0, errors.New("request timeout after PutParameter response was lost")
	}
	return version, nil
}

func (f *statefulParameterAPI) DescribeParameter(_ context.Context, name string) (ObservedState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if !f.exists || f.observed.ParameterName != name {
		return ObservedState{}, false, nil
	}
	return cloneParameterObserved(f.observed), true, nil
}

func (f *statefulParameterAPI) DeleteParameter(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.ParameterName != name {
		return errors.New("ParameterNotFound: parameter does not exist")
	}
	f.deletes++
	f.exists = false
	f.observed = ObservedState{}
	return nil
}

func (f *statefulParameterAPI) AddTags(_ context.Context, name string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.ParameterName != name {
		return errors.New("InvalidResourceId: parameter does not exist")
	}
	f.updates++
	if f.observed.Tags == nil {
		f.observed.Tags = map[string]string{}
	}
	maps.Copy(f.observed.Tags, tags)
	return nil
}

func (f *statefulParameterAPI) RemoveTags(_ context.Context, name string, tagKeys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.ParameterName != name {
		return errors.New("InvalidResourceId: parameter does not exist")
	}
	f.updates++
	for _, key := range tagKeys {
		delete(f.observed.Tags, key)
	}
	return nil
}

func (f *statefulParameterAPI) ListTags(_ context.Context, name string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.ParameterName != name {
		return nil, errors.New("InvalidResourceId: parameter does not exist")
	}
	return maps.Clone(f.observed.Tags), nil
}

func (f *statefulParameterAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.physicalCreates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulParameterAPI) parameter() ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneParameterObserved(f.observed)
}

func (f *statefulParameterAPI) seed(spec SSMParameterSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exists = true
	f.observed = observedFromParameterSpec(spec, 1)
}

func (f *statefulParameterAPI) forceCompositeDrift() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed.Type = "String"
	f.observed.Value = "stale-value"
	f.observed.Description = "stale description"
	f.observed.Tier = "Standard"
	f.observed.KmsKeyID = ""
	f.observed.AllowedPattern = ""
	f.observed.DataType = "text"
	f.observed.Tags = map[string]string{
		"env": "stale", "rogue": "remove", "praxis:managed-key": f.observed.Tags["praxis:managed-key"],
	}
}

func (f *statefulParameterAPI) forceDeleteExternally() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exists = false
	f.observed = ObservedState{}
}

type parameterDriftSink struct{}

func (parameterDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }
func (parameterDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error {
	return nil
}

func setupGenericParameter(t *testing.T, api SSMParameterAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericSSMParameterDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) SSMParameterAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(parameterDriftSink{})).Ingress()
}

func managedParameterSpec(name string) SSMParameterSpec {
	return SSMParameterSpec{
		Account: "test", Region: "us-east-1", ParameterName: name,
		Type: "SecureString", Value: "top-secret-value", Description: "application secret",
		Tier: "Advanced", KmsKeyID: "alias/app-key", AllowedPattern: ".+", DataType: "text",
		Tags: map[string]string{"env": "test"},
	}
}

func TestGenericSSMParameterCoreLifecycle(t *testing.T) {
	api := &statefulParameterAPI{}
	client := setupGenericParameter(t, api)
	spec := managedParameterSpec("/praxis/core/parameter")
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[SSMParameterSpec, SSMParameterOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~praxis-core-parameter", Spec: spec, Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, stored SSMParameterSpec) {
			assert.Equal(t, spec.Value, stored.Value)
			assert.Equal(t, "us-east-1~praxis-core-parameter", stored.ManagedKey)
		},
	})
}

func TestGenericSSMParameterObservedImportLifecycle(t *testing.T) {
	api := &statefulParameterAPI{}
	spec := managedParameterSpec("/praxis/imported/parameter")
	api.seed(spec)
	client := setupGenericParameter(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[SSMParameterOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~praxis-imported-parameter",
		Ref: types.ImportRef{ResourceID: spec.ParameterName, Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericSSMParameterVersionsIncrementOnlyOnChange(t *testing.T) {
	api := &statefulParameterAPI{}
	client := setupGenericParameter(t, api)
	key := "us-east-1~praxis-versioned"
	spec := managedParameterSpec("/praxis/versioned")

	first := provisionParameter(t, client, key, spec)
	second := provisionParameter(t, client, key, spec)
	assert.Equal(t, int64(1), first.Version)
	assert.Equal(t, first.Version, second.Version, "an in-sync Provision must not create a provider version")

	spec.Value = "rotated-secret"
	third := provisionParameter(t, client, key, spec)
	assert.Equal(t, int64(2), third.Version)
	assert.Equal(t, spec.Value, api.parameter().Value)
}

func TestGenericSSMParameterAmbiguousOverwriteUsesNativeRetryAndAcceptsVersionGap(t *testing.T) {
	api := &statefulParameterAPI{}
	client := setupGenericParameter(t, api)
	key := "us-east-1~praxis-ambiguous-overwrite"
	spec := managedParameterSpec("/praxis/ambiguous-overwrite")

	first := provisionParameter(t, client, key, spec)
	require.Equal(t, int64(1), first.Version)

	api.mu.Lock()
	api.failPutResponseOnce = true
	api.mu.Unlock()
	spec.Value = "rotated-after-ambiguous-response"
	updated := provisionParameter(t, client, key, spec)

	assert.Equal(t, int64(3), updated.Version, "the lost overwrite response consumes provider version 2 before Restate retries")
	assert.Equal(t, spec.Value, api.parameter().Value)
	api.mu.Lock()
	assert.Equal(t, 3, api.putAttempts)
	assert.Equal(t, 2, api.updates)
	api.mu.Unlock()
}

func TestGenericSSMParameterSecureValueBoundary(t *testing.T) {
	api := &statefulParameterAPI{}
	client := setupGenericParameter(t, api)
	key := "us-east-1~praxis-secure"
	spec := managedParameterSpec("/praxis/secure")
	outputs := provisionParameter(t, client, key, spec)
	status := getParameterStatus(t, client, key)
	inputs := getParameterInputs(t, client, key)

	assertJSONDoesNotContainParameter(t, outputs, spec.Value)
	assertJSONDoesNotContainParameter(t, status, spec.Value)
	assert.Equal(t, spec.Value, inputs.Value, "GetInputs preserves the existing explicit desired-state contract")
	assert.Equal(t, "SecureString", outputs.Type)
	assert.Equal(t, "alias/app-key", api.parameter().KmsKeyID)
}

func TestGenericSSMParameterAmbiguousCreateFailsClosedThenRecovers(t *testing.T) {
	api := &statefulParameterAPI{failPutResponseOnce: true}
	client := setupGenericParameter(t, api)
	key := "us-east-1~praxis-ambiguous"
	spec := managedParameterSpec("/praxis/ambiguous")

	_, err := ingress.Object[types.ProvisionRequest, SSMParameterOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err, "Parameter Store has no idempotency token; an ambiguous create must fail closed")
	assert.Equal(t, types.StatusError, getParameterStatus(t, client, key).Status)

	outputs := provisionParameter(t, client, key, spec)
	assert.Equal(t, int64(1), outputs.Version, "explicit recovery must observe the completed write rather than duplicate it")
	api.mu.Lock()
	assert.Equal(t, 1, api.physicalCreates)
	assert.Equal(t, 2, api.putAttempts, "Restate retried once and the provider collision stopped unsafe replay")
	api.mu.Unlock()
}

func TestGenericSSMParameterConvergesAllMutableFields(t *testing.T) {
	api := &statefulParameterAPI{}
	client := setupGenericParameter(t, api)
	key := "us-east-1~praxis-mutable"
	spec := managedParameterSpec("/praxis/mutable")
	provisionParameter(t, client, key, spec)
	api.forceCompositeDrift()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assertParameterMatchesSpec(t, spec, api.parameter())
	assert.Equal(t, int64(2), api.parameter().Version)
}

func TestGenericSSMParameterRejectsImmutableNameChange(t *testing.T) {
	api := &statefulParameterAPI{}
	client := setupGenericParameter(t, api)
	key := "us-east-1~praxis-immutable"
	spec := managedParameterSpec("/praxis/immutable")
	provisionParameter(t, client, key, spec)
	spec.ParameterName = "/praxis/different"

	_, err := ingress.Object[types.ProvisionRequest, SSMParameterOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parameterName is immutable")
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericSSMParameterExternalDeleteRequiresReplacementWithoutCreation(t *testing.T) {
	api := &statefulParameterAPI{}
	client := setupGenericParameter(t, api)
	key := "us-east-1~praxis-external-delete"
	spec := managedParameterSpec("/praxis/external-delete")
	provisionParameter(t, client, key, spec)
	api.forceDeleteExternally()
	before := api.snapshot()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "deleted externally")
	after := api.snapshot()
	assert.Equal(t, before.Creates, after.Creates)
	assert.Equal(t, types.StatusError, getParameterStatus(t, client, key).Status)
}

func provisionParameter(t *testing.T, client *ingress.Client, key string, spec SSMParameterSpec) SSMParameterOutputs {
	t.Helper()
	outputs, err := ingress.Object[types.ProvisionRequest, SSMParameterOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	return outputs
}

func getParameterStatus(t *testing.T, client *ingress.Client, key string) types.StatusResponse {
	t.Helper()
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return status
}

func getParameterInputs(t *testing.T, client *ingress.Client, key string) SSMParameterSpec {
	t.Helper()
	inputs, err := ingress.Object[restate.Void, SSMParameterSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return inputs
}

func assertJSONDoesNotContainParameter(t *testing.T, value any, sensitive string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), sensitive)
}

func assertParameterMatchesSpec(t *testing.T, spec SSMParameterSpec, observed ObservedState) {
	t.Helper()
	assert.Equal(t, spec.ParameterName, observed.ParameterName)
	assert.Equal(t, spec.Type, observed.Type)
	assert.Equal(t, spec.Value, observed.Value)
	assert.Equal(t, spec.Description, observed.Description)
	assert.Equal(t, spec.Tier, observed.Tier)
	assert.Equal(t, spec.KmsKeyID, observed.KmsKeyID)
	assert.Equal(t, spec.AllowedPattern, observed.AllowedPattern)
	assert.Equal(t, spec.DataType, observed.DataType)
	assert.Equal(t, spec.Tags, drivers.FilterPraxisTags(observed.Tags))
}

func observedFromParameterSpec(spec SSMParameterSpec, version int64) ObservedState {
	defaults := applyDefaults(spec)
	kmsKeyID := defaults.KmsKeyID
	if defaults.Type == "SecureString" && kmsKeyID == "" {
		kmsKeyID = "alias/aws/ssm"
	}
	return ObservedState{
		ARN:           "arn:aws:ssm:us-east-1:123456789012:parameter" + defaults.ParameterName,
		ParameterName: defaults.ParameterName, Type: defaults.Type, Value: defaults.Value,
		Description: defaults.Description, Tier: defaults.Tier, KmsKeyID: kmsKeyID,
		AllowedPattern: defaults.AllowedPattern, DataType: defaults.DataType, Version: version,
		Tags: managedTags(defaults.Tags, defaults.ManagedKey),
	}
}

func cloneParameterObserved(observed ObservedState) ObservedState {
	observed.Tags = maps.Clone(observed.Tags)
	return observed
}
