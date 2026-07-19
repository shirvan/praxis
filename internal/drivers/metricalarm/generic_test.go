package metricalarm

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

type statefulMetricAlarmAPI struct {
	mu sync.Mutex

	exists   bool
	observed ObservedState

	creates       int
	reads         int
	updates       int
	deletes       int
	tagWrites     int
	putErrors     []error
	tagReadErrors []error
	failTagOnce   bool
	lastPutSpecs  []MetricAlarmSpec
}

type metricAlarmDriftSink struct{}

func (metricAlarmDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }

func (metricAlarmDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error {
	return nil
}

func (f *statefulMetricAlarmAPI) PutMetricAlarm(_ context.Context, spec MetricAlarmSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastPutSpecs = append(f.lastPutSpecs, cloneSpec(spec))
	wasExisting := f.exists
	if wasExisting {
		f.updates++
	} else {
		f.creates++
	}
	tags := maps.Clone(f.observed.Tags)
	f.exists = true
	f.observed = observedFromSpec(spec, tags)
	if len(f.putErrors) > 0 {
		err := f.putErrors[0]
		f.putErrors = f.putErrors[1:]
		return err
	}
	return nil
}

func (f *statefulMetricAlarmAPI) DescribeAlarm(_ context.Context, name string) (ObservedState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if !f.exists || f.observed.AlarmName != name {
		return ObservedState{}, false, nil
	}
	return cloneObserved(f.observed), true, nil
}

func (f *statefulMetricAlarmAPI) DeleteAlarm(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists || f.observed.AlarmName != name {
		return errors.New("ResourceNotFound: alarm is gone")
	}
	f.deletes++
	f.exists = false
	f.observed = ObservedState{}
	return nil
}

func (f *statefulMetricAlarmAPI) TagResource(_ context.Context, _ string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failTagOnce {
		f.failTagOnce = false
		return errors.New("InvalidParameterValue: injected tag failure")
	}
	f.tagWrites++
	if f.observed.Tags == nil {
		f.observed.Tags = map[string]string{}
	}
	maps.Copy(f.observed.Tags, tags)
	return nil
}

func (f *statefulMetricAlarmAPI) UntagResource(_ context.Context, _ string, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tagWrites++
	for _, key := range keys {
		delete(f.observed.Tags, key)
	}
	return nil
}

func (f *statefulMetricAlarmAPI) ListTagsForResource(context.Context, string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tagReadErrors) > 0 {
		err := f.tagReadErrors[0]
		f.tagReadErrors = f.tagReadErrors[1:]
		return nil, err
	}
	return maps.Clone(f.observed.Tags), nil
}

func (f *statefulMetricAlarmAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{
		Creates: f.creates, Reads: f.reads, Updates: f.updates + f.tagWrites, Deletes: f.deletes,
	}
}

func (f *statefulMetricAlarmAPI) alarm() ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneObserved(f.observed)
}

func (f *statefulMetricAlarmAPI) removeExternally() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exists = false
	f.observed = ObservedState{}
}

func (f *statefulMetricAlarmAPI) injectDrift() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed.Dimensions = map[string]string{"InstanceId": "i-drifted", "Zone": "wrong"}
	f.observed.Threshold = 99
	f.observed.AlarmActions = []string{"arn:wrong"}
	f.observed.OKActions = []string{"arn:stale"}
	f.observed.Tags = map[string]string{"env": "dev", "stale": "remove", "praxis:managed-key": "wrong-key"}
}

func setupGenericMetricAlarm(t *testing.T, api MetricAlarmAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericMetricAlarmDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) MetricAlarmAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(metricAlarmDriftSink{})).Ingress()
}

func managedAlarmSpec(name string) MetricAlarmSpec {
	datapoints := int32(2)
	return MetricAlarmSpec{
		Account: "test", Region: "us-east-1", AlarmName: name,
		Namespace: "AWS/EC2", MetricName: "CPUUtilization",
		Dimensions: map[string]string{"InstanceId": "i-123"}, Statistic: "Average",
		Period: 60, EvaluationPeriods: 3, DatapointsToAlarm: &datapoints,
		Threshold: 80, ComparisonOperator: "GreaterThanThreshold", TreatMissingData: "missing",
		AlarmDescription: "CPU is high", ActionsEnabled: true,
		AlarmActions: []string{"arn:aws:sns:us-east-1:123456789012:alarm", "arn:aws:sns:us-east-1:123456789012:audit"},
		OKActions:    []string{"arn:aws:sns:us-east-1:123456789012:ok"},
		Tags:         map[string]string{"env": "prod", "team": "platform"},
	}
}

func TestGenericMetricAlarmCoreLifecycle(t *testing.T) {
	api := &statefulMetricAlarmAPI{}
	client := setupGenericMetricAlarm(t, api)
	spec := managedAlarmSpec("generic-alarm")
	key := "us-east-1~generic-alarm"
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[MetricAlarmSpec, MetricAlarmOutputs]{
		Client: client, ServiceName: ServiceName, Key: key, Spec: spec, Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs MetricAlarmSpec) {
			assert.Equal(t, key, inputs.ManagedKey)
			assert.Equal(t, sortedCopy(spec.AlarmActions), inputs.AlarmActions)
		},
	})
}

func TestGenericMetricAlarmObservedImportLifecycle(t *testing.T) {
	spec := managedAlarmSpec("existing-alarm")
	api := &statefulMetricAlarmAPI{exists: true, observed: observedFromSpec(spec, spec.Tags)}
	client := setupGenericMetricAlarm(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[MetricAlarmOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~existing-alarm",
		Ref: types.ImportRef{ResourceID: spec.AlarmName, Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericMetricAlarmReconcileConvergesConfigurationActionsAndTags(t *testing.T) {
	api := &statefulMetricAlarmAPI{}
	client := setupGenericMetricAlarm(t, api)
	spec := managedAlarmSpec("drift-alarm")
	key := "us-east-1~drift-alarm"
	_, err := ingress.Object[types.ProvisionRequest, MetricAlarmOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	before := api.snapshot()
	api.injectDrift()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	after := api.snapshot()
	assert.Greater(t, after.Updates, before.Updates)
	assertAlarmMatchesSpec(t, spec, api.alarm(), key)
}

func TestGenericMetricAlarmRejectsImmutableNameChange(t *testing.T) {
	api := &statefulMetricAlarmAPI{}
	client := setupGenericMetricAlarm(t, api)
	spec := managedAlarmSpec("original-alarm")
	key := "us-east-1~original-alarm"
	_, err := ingress.Object[types.ProvisionRequest, MetricAlarmOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	before := api.snapshot()
	spec.AlarmName = "different-alarm"
	_, err = ingress.Object[types.ProvisionRequest, MetricAlarmOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "alarmName is immutable")
	assert.Equal(t, before.Updates, api.snapshot().Updates)
}

func TestGenericMetricAlarmRecoversPartialCreateWithoutSecondAlarm(t *testing.T) {
	api := &statefulMetricAlarmAPI{failTagOnce: true}
	client := setupGenericMetricAlarm(t, api)
	spec := managedAlarmSpec("partial-alarm")
	key := "us-east-1~partial-alarm"
	_, err := ingress.Object[types.ProvisionRequest, MetricAlarmOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
	assert.Equal(t, spec.AlarmName, api.alarm().AlarmName)

	_, err = ingress.Object[types.ProvisionRequest, MetricAlarmOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, 1, api.snapshot().Creates)
	assertAlarmMatchesSpec(t, spec, api.alarm(), key)
}

func TestGenericMetricAlarmRetriesAmbiguousPutByName(t *testing.T) {
	api := &statefulMetricAlarmAPI{putErrors: []error{errors.New("ServiceUnavailable: response lost after PutMetricAlarm")}}
	client := setupGenericMetricAlarm(t, api)
	spec := managedAlarmSpec("response-loss-alarm")
	key := "us-east-1~response-loss-alarm"
	outputs, err := ingress.Object[types.ProvisionRequest, MetricAlarmOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.AlarmArn)
	snapshot := api.snapshot()
	assert.Equal(t, 1, snapshot.Creates)
	assert.GreaterOrEqual(t, snapshot.Updates, 1, "retry replays the name-idempotent PutMetricAlarm request")
}

func TestGenericMetricAlarmRetriesTransientTagObservation(t *testing.T) {
	spec := applyDefaults(managedAlarmSpec("tag-read-retry"))
	api := &statefulMetricAlarmAPI{
		exists: true, observed: observedFromSpec(spec, managedTags(spec.Tags, "us-east-1~tag-read-retry")),
		tagReadErrors: []error{errors.New("ServiceUnavailable: tag read failed transiently")},
	}
	client := setupGenericMetricAlarm(t, api)
	_, err := ingress.Object[types.ProvisionRequest, MetricAlarmOutputs](
		client, ServiceName, "us-east-1~tag-read-retry", "Provision",
	).Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Zero(t, api.snapshot().Creates)
	assert.GreaterOrEqual(t, api.snapshot().Reads, 2, "the entire durable observation must retry")
}

func TestGenericMetricAlarmExternalDeleteIsVisibilityOnly(t *testing.T) {
	api := &statefulMetricAlarmAPI{}
	client := setupGenericMetricAlarm(t, api)
	spec := managedAlarmSpec("deleted-alarm")
	key := "us-east-1~deleted-alarm"
	_, err := ingress.Object[types.ProvisionRequest, MetricAlarmOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	creates := api.snapshot().Creates
	api.removeExternally()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "MetricAlarm resource was deleted externally")
	assert.Equal(t, creates, api.snapshot().Creates)
}

func observedFromSpec(spec MetricAlarmSpec, tags map[string]string) ObservedState {
	return normalizeObserved(ObservedState{
		AlarmArn:  "arn:aws:cloudwatch:us-east-1:123456789012:alarm:" + spec.AlarmName,
		AlarmName: spec.AlarmName, Namespace: spec.Namespace, MetricName: spec.MetricName,
		Dimensions: maps.Clone(spec.Dimensions), Statistic: spec.Statistic, ExtendedStatistic: spec.ExtendedStatistic,
		Period: spec.Period, EvaluationPeriods: spec.EvaluationPeriods,
		DatapointsToAlarm: func() int32 {
			if spec.DatapointsToAlarm == nil {
				return 0
			}
			return *spec.DatapointsToAlarm
		}(),
		Threshold: spec.Threshold, ComparisonOperator: spec.ComparisonOperator, TreatMissingData: spec.TreatMissingData,
		AlarmDescription: spec.AlarmDescription, ActionsEnabled: spec.ActionsEnabled,
		AlarmActions: slices.Clone(spec.AlarmActions), OKActions: slices.Clone(spec.OKActions),
		InsufficientDataActions: slices.Clone(spec.InsufficientDataActions), Unit: spec.Unit,
		StateValue: "INSUFFICIENT_DATA", StateReason: "Unchecked: Initial alarm creation", Tags: maps.Clone(tags),
	})
}

func cloneObserved(input ObservedState) ObservedState {
	input.Dimensions = maps.Clone(input.Dimensions)
	input.AlarmActions = slices.Clone(input.AlarmActions)
	input.OKActions = slices.Clone(input.OKActions)
	input.InsufficientDataActions = slices.Clone(input.InsufficientDataActions)
	input.Tags = maps.Clone(input.Tags)
	return input
}

func cloneSpec(input MetricAlarmSpec) MetricAlarmSpec {
	input.Dimensions = maps.Clone(input.Dimensions)
	input.AlarmActions = slices.Clone(input.AlarmActions)
	input.OKActions = slices.Clone(input.OKActions)
	input.InsufficientDataActions = slices.Clone(input.InsufficientDataActions)
	input.Tags = maps.Clone(input.Tags)
	if input.DatapointsToAlarm != nil {
		value := *input.DatapointsToAlarm
		input.DatapointsToAlarm = &value
	}
	return input
}

func assertAlarmMatchesSpec(t *testing.T, spec MetricAlarmSpec, observed ObservedState, managedKey string) {
	t.Helper()
	assert.False(t, hasConfigurationDrift(applyDefaults(spec), observed))
	assert.Equal(t, managedTags(spec.Tags, managedKey), observed.Tags)
}
