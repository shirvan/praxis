package kernel_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	"github.com/shirvan/praxis/internal/drivers/kernel"
	"github.com/shirvan/praxis/internal/eventing"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type testSpec struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret string `json:"secret,omitempty"`
}

type testOutputs struct {
	ID      string `json:"id"`
	OneTime string `json:"oneTime,omitempty"`
}

type testObserved struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Value  string `json:"value"`
	Ready  bool   `json:"ready"`
	Failed bool   `json:"failed"`
}

type fakeOperations struct {
	mu sync.Mutex

	observed testObserved
	creates  int
	reads    int
	updates  int
	deletes  int

	createThenFail          bool
	deleteThenFail          bool
	failObserve             bool
	createDefault           string
	createOnly              string
	convergeErr             error
	convergeErrOnce         error
	convergeOutput          testOutputs
	observeAfterConvergeErr error
	nextObserveErr          error

	provisionChanges   int
	previousDesired    testSpec
	nextDesired        testSpec
	provisionChangeErr error
	provisionChangeOut testOutputs
}

type driftSink struct{}

func (driftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }

func (driftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

func (f *fakeOperations) Observe(ctx restate.ObjectContext, desired testSpec, outputs testOutputs) (kernel.Observation[testObserved], error) {
	return drivers.RunAWS(ctx, func(restate.RunContext) (kernel.Observation[testObserved], error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reads++
		if f.nextObserveErr != nil {
			err := f.nextObserveErr
			f.nextObserveErr = nil
			return kernel.Observation[testObserved]{}, err
		}
		if f.failObserve {
			f.failObserve = false
			return kernel.Observation[testObserved]{}, errors.New("provider read failed")
		}
		if f.observed.ID == "" {
			return kernel.Observation[testObserved]{}, nil
		}
		return kernel.Observation[testObserved]{Exists: true, Value: f.observed}, nil
	}, terminalForTest)
}

func (f *fakeOperations) Create(ctx restate.ObjectContext, desired testSpec) (kernel.CreateResult[testOutputs], error) {
	return drivers.RunAWS(ctx, func(restate.RunContext) (kernel.CreateResult[testOutputs], error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.creates++
		value := desired.Value
		if value == "" {
			value = f.createDefault
		}
		f.observed = testObserved{ID: "provider-" + desired.Name, Name: desired.Name, Value: value}
		if f.createThenFail {
			f.createThenFail = false
			return kernel.CreateResult[testOutputs]{}, errors.New("response lost after create")
		}
		result := kernel.CreateResult[testOutputs]{SeedOutputs: testOutputs{ID: f.observed.ID}}
		if f.createOnly != "" {
			response := testOutputs{ID: f.observed.ID, OneTime: f.createOnly}
			result.CreateOnlyResponse = &response
		}
		return result, nil
	}, terminalForTest)
}

func (f *fakeOperations) ConvergeProvisionChange(_ restate.ObjectContext, previousDesired, nextDesired testSpec, _ testObserved, currentOutputs testOutputs) (testOutputs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.provisionChanges++
	f.previousDesired = previousDesired
	f.nextDesired = nextDesired
	if f.provisionChangeErr != nil {
		return currentOutputs, f.provisionChangeErr
	}
	if f.provisionChangeOut.ID != "" {
		f.observed.ID = f.provisionChangeOut.ID
		return f.provisionChangeOut, nil
	}
	return currentOutputs, nil
}

func (f *fakeOperations) Converge(ctx restate.ObjectContext, desired testSpec, _ testObserved, currentOutputs testOutputs) (testOutputs, error) {
	f.mu.Lock()
	convergeErr := f.convergeErr
	if f.convergeErrOnce != nil {
		convergeErr = f.convergeErrOnce
		f.convergeErrOnce = nil
	}
	convergeOutput := f.convergeOutput
	f.mu.Unlock()
	if convergeErr != nil {
		return currentOutputs, convergeErr
	}
	_, err := drivers.RunAWS(ctx, func(restate.RunContext) (restate.Void, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.updates++
		f.observed.Name = desired.Name
		f.observed.Value = desired.Value
		if convergeOutput.ID != "" {
			f.observed.ID = convergeOutput.ID
		}
		f.nextObserveErr = f.observeAfterConvergeErr
		return restate.Void{}, nil
	}, terminalForTest)
	if err != nil || convergeOutput.ID == "" {
		return currentOutputs, err
	}
	return convergeOutput, nil
}

func (f *fakeOperations) Delete(ctx restate.ObjectContext, _ testSpec, _ testOutputs) error {
	_, err := drivers.RunAWS(ctx, func(restate.RunContext) (restate.Void, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.deletes++
		f.observed = testObserved{}
		if f.deleteThenFail {
			f.deleteThenFail = false
			return restate.Void{}, errors.New("response lost after delete")
		}
		return restate.Void{}, nil
	}, terminalForTest)
	return err
}

func (f *fakeOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[testObserved], error) {
	return drivers.RunAWS(ctx, func(restate.RunContext) (kernel.Observation[testObserved], error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reads++
		if f.observed.ID == "" || (ref.ResourceID != f.observed.ID && ref.ResourceID != f.observed.Name) {
			return kernel.Observation[testObserved]{}, nil
		}
		return kernel.Observation[testObserved]{Exists: true, Value: f.observed}, nil
	}, terminalForTest)
}

var errTransientProvider = errors.New("transient provider failure")

func terminalForTest(err error) error {
	if restate.IsTerminalError(err) || errors.Is(err, errTransientProvider) {
		return err
	}
	return restate.TerminalError(err, 500)
}

func (f *fakeOperations) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *fakeOperations) provisionChangeSnapshot() (int, testSpec, testSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.provisionChanges, f.previousDesired, f.nextDesired
}

func (f *fakeOperations) failProvisionChange(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.provisionChangeErr = err
}

func (f *fakeOperations) failConverge(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.convergeErr = err
}

func (f *fakeOperations) failConvergeOnce(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.convergeErrOnce = err
}

func (f *fakeOperations) failObserveAfterConverge(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observeAfterConvergeErr = err
}

func (f *fakeOperations) mutate(value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed.Value = value
}

func (f *fakeOperations) setReady() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed.Ready = true
}

func (f *fakeOperations) remove() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed = testObserved{}
}

func testDescriptor(ops *fakeOperations) kernel.Descriptor[testSpec, testOutputs, testObserved] {
	return kernel.Descriptor[testSpec, testOutputs, testObserved]{
		ServiceName: "KernelTestResource",
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
		},
		Operations: ops,
		Prepare: func(_ restate.ObjectContext, spec testSpec) (testSpec, error) {
			spec.Name = strings.TrimSpace(spec.Name)
			return spec, nil
		},
		Validate: func(spec testSpec) error {
			if spec.Name == "" {
				return errors.New("name is required")
			}
			return nil
		},
		DesiredFromObserved: func(_ types.ImportRef, observed testObserved) testSpec {
			return testSpec{Name: observed.Name, Value: observed.Value}
		},
		OutputsFromObserved: func(observed testObserved, _ testOutputs) testOutputs {
			return testOutputs{ID: observed.ID}
		},
		HasDrift: func(desired testSpec, observed testObserved) bool {
			return desired.Name != observed.Name || desired.Value != observed.Value
		},
		FieldDiffs: func(desired testSpec, observed testObserved) []types.FieldDiff {
			var diffs []types.FieldDiff
			if desired.Name != observed.Name {
				diffs = append(diffs, types.FieldDiff{Path: "spec.name", OldValue: observed.Name, NewValue: desired.Name})
			}
			if desired.Value != observed.Value {
				diffs = append(diffs, types.FieldDiff{Path: "spec.value", OldValue: observed.Value, NewValue: desired.Value})
			}
			return diffs
		},
	}
}

func setupKernelDriver(t *testing.T, ops *fakeOperations) *ingress.Client {
	t.Helper()
	driver := kernel.MustNew(testDescriptor(ops))
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(driftSink{})).Ingress()
}

func provision(t *testing.T, client *ingress.Client, key string, spec testSpec) (testOutputs, error) {
	t.Helper()
	return provisionWithLifecycle(t, client, key, spec, types.LifecyclePolicy{Reconcile: types.ReconcileModeAuto})
}

func provisionWithLifecycle(t *testing.T, client *ingress.Client, key string, spec testSpec, lifecycle types.LifecyclePolicy) (testOutputs, error) {
	t.Helper()
	encoded, err := json.Marshal(spec)
	require.NoError(t, err)
	request := types.ProvisionRequest{Spec: encoded, Lifecycle: lifecycle}
	return ingress.Object[types.ProvisionRequest, testOutputs](client, "KernelTestResource", key, "Provision").Request(t.Context(), request)
}

func status(t *testing.T, client *ingress.Client, key string) types.StatusResponse {
	t.Helper()
	got, err := ingress.Object[restate.Void, types.StatusResponse](client, "KernelTestResource", key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return got
}

func TestGenericDriverCoreLifecycle(t *testing.T) {
	ops := &fakeOperations{}
	client := setupKernelDriver(t, ops)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[testSpec, testOutputs]{
		Client: client, ServiceName: "KernelTestResource", Key: "managed",
		Spec: testSpec{Name: "resource", Value: "desired"}, Snapshot: ops.snapshot,
	})
}

func TestGenericDriverObservedImportLifecycle(t *testing.T) {
	ops := &fakeOperations{observed: testObserved{ID: "provider-existing", Name: "existing", Value: "current"}}
	client := setupKernelDriver(t, ops)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[testOutputs]{
		Client: client, ServiceName: "KernelTestResource", Key: "observed",
		Ref: types.ImportRef{ResourceID: "provider-existing"}, Snapshot: ops.snapshot,
	})
}

func TestImportValidationDefaultsToProvisionValidation(t *testing.T) {
	ops := &fakeOperations{observed: testObserved{ID: "provider-detached", Value: "current"}}
	descriptor := testDescriptor(ops)
	client := restatetest.Start(t, restate.Reflect(kernel.MustNew(descriptor)), restate.Reflect(driftSink{})).Ingress()

	_, err := ingress.Object[types.ImportRef, testOutputs](client, "KernelTestResource", "default-import-validation", "Import").Request(
		t.Context(), types.ImportRef{ResourceID: "provider-detached"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestImportValidationCanBeNarrowerWithoutWeakeningProvision(t *testing.T) {
	ops := &fakeOperations{observed: testObserved{ID: "provider-detached", Value: "current"}}
	descriptor := testDescriptor(ops)
	descriptor.ValidateImport = func(testSpec) error { return nil }
	client := restatetest.Start(t, restate.Reflect(kernel.MustNew(descriptor)), restate.Reflect(driftSink{})).Ingress()

	outputs, err := ingress.Object[types.ImportRef, testOutputs](client, "KernelTestResource", "narrow-import-validation", "Import").Request(
		t.Context(), types.ImportRef{ResourceID: "provider-detached"},
	)
	require.NoError(t, err)
	assert.Equal(t, "provider-detached", outputs.ID)

	_, err = provision(t, client, "strict-provision-validation", testSpec{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestCreateResponseLossIsRecoveredWithoutDuplicateCreate(t *testing.T) {
	ops := &fakeOperations{createThenFail: true}
	client := setupKernelDriver(t, ops)
	spec := testSpec{Name: "adopt-after-fault", Value: "desired"}

	_, err := provision(t, client, "create-fault", spec)
	require.Error(t, err)
	assert.Equal(t, types.StatusError, status(t, client, "create-fault").Status)
	assert.Equal(t, 1, ops.snapshot().Creates)

	outputs, err := provision(t, client, "create-fault", spec)
	require.NoError(t, err)
	assert.Equal(t, "provider-adopt-after-fault", outputs.ID)
	assert.Equal(t, 1, ops.snapshot().Creates, "retry must observe and adopt the already-created provider resource")
	assert.Equal(t, types.StatusReady, status(t, client, "create-fault").Status)
}

func TestProvisionChangeHookUsesInvocationLocalPreviousDesiredOnlyOnProvision(t *testing.T) {
	ops := &fakeOperations{}
	client := setupKernelDriver(t, ops)
	key := "write-only-change"

	_, err := provision(t, client, key, testSpec{Name: "resource", Value: "visible", Secret: "old"})
	require.NoError(t, err)
	count, _, _ := ops.provisionChangeSnapshot()
	assert.Zero(t, count, "initial creation has no previous desired generation")

	_, err = provision(t, client, key, testSpec{Name: "resource", Value: "visible", Secret: "new"})
	require.NoError(t, err)
	count, previous, next := ops.provisionChangeSnapshot()
	assert.Equal(t, 1, count, "existing resources invoke the Provision-only hook even without observable drift")
	assert.Equal(t, "old", previous.Secret)
	assert.Equal(t, "new", next.Secret)
	assert.Zero(t, ops.snapshot().Updates, "ordinary Converge remains unnecessary when observable state matches")

	stored, err := ingress.Object[restate.Void, testSpec](client, "KernelTestResource", key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, "new", stored.Secret)
	encoded, err := json.Marshal(stored)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `"previousDesired"`, "State stores only the current desired contract")

	_, err = ingress.Object[restate.Void, types.ReconcileResult](client, "KernelTestResource", key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	count, _, _ = ops.provisionChangeSnapshot()
	assert.Equal(t, 1, count, "Reconcile must never invoke the previous/next Provision hook")
}

func TestProvisionChangeRejectionRetainsLastAcceptedDesired(t *testing.T) {
	ops := &fakeOperations{}
	client := setupKernelDriver(t, ops)
	key := "rejected-write-only-change"
	accepted := testSpec{Name: "resource", Value: "visible", Secret: "old"}

	_, err := provision(t, client, key, accepted)
	require.NoError(t, err)
	ops.failProvisionChange(restate.TerminalError(errors.New("secret is immutable"), 409))

	_, err = provision(t, client, key, testSpec{Name: "resource", Value: "visible", Secret: "rejected"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret is immutable")

	stored, getErr := ingress.Object[restate.Void, testSpec](client, "KernelTestResource", key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, getErr)
	assert.Equal(t, accepted, stored, "a rejected Provision change must not replace the last accepted desired contract")
	assert.Equal(t, types.StatusError, status(t, client, key).Status)
}

func TestProvisionConvergeConflictRetainsLastAcceptedDesired(t *testing.T) {
	ops := &fakeOperations{}
	client := setupKernelDriver(t, ops)
	key := "rejected-observable-change"
	accepted := testSpec{Name: "resource", Value: "accepted"}

	_, err := provision(t, client, key, accepted)
	require.NoError(t, err)
	ops.failConverge(restate.TerminalError(errors.New("provider identity is immutable"), 409))

	_, err = provision(t, client, key, testSpec{Name: "resource", Value: "rejected"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider identity is immutable")

	stored, getErr := ingress.Object[restate.Void, testSpec](client, "KernelTestResource", key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, getErr)
	assert.Equal(t, accepted, stored, "a terminal 409 from ordinary Converge must not replace the last accepted desired contract")
	assert.Equal(t, types.StatusError, status(t, client, key).Status)
}

func TestProvisionConvergeNonConflictKeepsAttemptedDesired(t *testing.T) {
	ops := &fakeOperations{}
	client := setupKernelDriver(t, ops)
	key := "rejected-non-conflict-change"

	_, err := provision(t, client, key, testSpec{Name: "resource", Value: "accepted"})
	require.NoError(t, err)
	ops.failConverge(restate.TerminalError(errors.New("invalid mutable value"), 400))
	attempted := testSpec{Name: "resource", Value: "attempted"}

	_, err = provision(t, client, key, attempted)
	require.Error(t, err)
	stored, getErr := ingress.Object[restate.Void, testSpec](client, "KernelTestResource", key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, getErr)
	assert.Equal(t, attempted, stored, "non-409 Provision failure persistence must remain unchanged")
}

func TestCreateOnlyResponseIsReturnedOnceAndNeverStoredInStateOutputs(t *testing.T) {
	const oneTime = "one-time-private-material"
	ops := &fakeOperations{createOnly: oneTime}
	client := setupKernelDriver(t, ops)
	key := "create-only-response"
	spec := testSpec{Name: "resource", Value: "visible"}

	first, err := provision(t, client, key, spec)
	require.NoError(t, err)
	assert.Equal(t, oneTime, first.OneTime)
	assert.Equal(t, types.StatusReady, status(t, client, key).Status, "one-shot response is returned after Ready state is committed")

	persisted, err := ingress.Object[restate.Void, testOutputs](client, "KernelTestResource", key, "GetOutputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Empty(t, persisted.OneTime)
	rawStateOutputs, err := json.Marshal(persisted)
	require.NoError(t, err)
	assert.NotContains(t, string(rawStateOutputs), oneTime, "StateKey-backed outputs must not contain the create-only response")

	second, err := provision(t, client, key, spec)
	require.NoError(t, err)
	assert.Empty(t, second.OneTime, "an existing resource never replays CreateOnlyResponse on a later Provision")
}

func TestDeleteResponseLossRetainsRecoverableErrorStateAndTombstonesOnRetry(t *testing.T) {
	ops := &fakeOperations{deleteThenFail: true}
	client := setupKernelDriver(t, ops)
	_, err := provision(t, client, "delete-fault", testSpec{Name: "resource", Value: "desired"})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, "KernelTestResource", "delete-fault", "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Equal(t, types.StatusError, status(t, client, "delete-fault").Status)

	_, err = ingress.Object[restate.Void, restate.Void](client, "KernelTestResource", "delete-fault", "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusDeleted, status(t, client, "delete-fault").Status)
	assert.Equal(t, 2, ops.snapshot().Deletes, "idempotent provider delete is retried after an ambiguous response")
}

func TestManagedReconcileCorrectsDriftAndObservedReconcileDoesNot(t *testing.T) {
	t.Run("managed", func(t *testing.T) {
		ops := &fakeOperations{}
		client := setupKernelDriver(t, ops)
		_, err := provision(t, client, "managed-drift", testSpec{Name: "resource", Value: "desired"})
		require.NoError(t, err)
		ops.mutate("drifted")

		result, err := ingress.Object[restate.Void, types.ReconcileResult](client, "KernelTestResource", "managed-drift", "Reconcile").Request(t.Context(), restate.Void{})
		require.NoError(t, err)
		assert.True(t, result.Drift)
		assert.True(t, result.Correcting)
		assert.Equal(t, 1, ops.snapshot().Updates)
	})

	t.Run("observed", func(t *testing.T) {
		ops := &fakeOperations{observed: testObserved{ID: "provider-existing", Name: "existing", Value: "current"}}
		client := setupKernelDriver(t, ops)
		_, err := ingress.Object[types.ImportRef, testOutputs](client, "KernelTestResource", "observed-drift", "Import").Request(t.Context(), types.ImportRef{ResourceID: "provider-existing"})
		require.NoError(t, err)
		ops.mutate("drifted")

		result, err := ingress.Object[restate.Void, types.ReconcileResult](client, "KernelTestResource", "observed-drift", "Reconcile").Request(t.Context(), restate.Void{})
		require.NoError(t, err)
		assert.True(t, result.Drift)
		assert.False(t, result.Correcting)
		assert.Zero(t, ops.snapshot().Updates)
	})
}

func TestReconcileCorrectionErrorsUseRestateRetryClassification(t *testing.T) {
	for _, pending := range []bool{false, true} {
		pathName := "ordinary"
		if pending {
			pathName = "pending"
		}
		for _, stage := range []string{"converge", "follow-observe"} {
			for _, terminal := range []bool{false, true} {
				className := "transient"
				if terminal {
					className = "terminal"
				}
				t.Run(pathName+"/"+stage+"/"+className, func(t *testing.T) {
					ops := &fakeOperations{}
					descriptor := testDescriptor(ops)
					if pending {
						descriptor.Capabilities.Readiness = true
						descriptor.Capabilities.ConvergeWhilePending = true
						descriptor.CheckReadiness = func(testObserved) kernel.ReadinessResult {
							return kernel.ReadinessResult{Phase: kernel.ReadinessPending, Message: "provider is still starting"}
						}
					}
					client := restatetest.Start(t, restate.Reflect(kernel.MustNew(descriptor)), restate.Reflect(driftSink{})).Ingress()
					key := pathName + "-" + stage + "-" + className
					_, err := provision(t, client, key, testSpec{Name: "resource", Value: "desired"})
					require.NoError(t, err)
					baseline := ops.snapshot()
					if !pending {
						ops.mutate("drifted")
					}

					failure := error(errTransientProvider)
					if terminal {
						failure = restate.TerminalError(errors.New("terminal correction failure"), 409)
					}
					switch stage {
					case "converge":
						if terminal {
							ops.failConverge(failure)
						} else {
							ops.failConvergeOnce(failure)
						}
					case "follow-observe":
						ops.failObserveAfterConverge(failure)
					}

					result, requestErr := ingress.Object[restate.Void, types.ReconcileResult](client, "KernelTestResource", key, "Reconcile").Request(t.Context(), restate.Void{})
					if terminal {
						require.NoError(t, requestErr, "terminal correction failures are durable visibility results")
						assert.Contains(t, result.Error, "terminal correction failure")
						assert.Equal(t, types.StatusError, status(t, client, key).Status)
						expectedUpdates := baseline.Updates
						if stage == "follow-observe" {
							expectedUpdates++
						}
						assert.Equal(t, expectedUpdates, ops.snapshot().Updates)
						return
					}

					require.NoError(t, requestErr, "Restate must retry transient correction failures")
					assert.Empty(t, result.Error)
					after := ops.snapshot()
					assert.Equal(t, baseline.Updates+1, after.Updates, "journal replay must not repeat the provider write")
					if stage == "follow-observe" {
						assert.GreaterOrEqual(t, after.Reads, baseline.Reads+3, "the transient confirming read must execute again, not replay a failed result forever")
					}
					expectedStatus := types.StatusReady
					if pending {
						expectedStatus = types.StatusPending
					}
					assert.Equal(t, expectedStatus, status(t, client, key).Status)
				})
			}
		}
	}
}

func TestObserveReconcileReportsDriftWithoutProviderWrites(t *testing.T) {
	ops := &fakeOperations{}
	client := setupKernelDriver(t, ops)
	lifecycle := types.LifecyclePolicy{Reconcile: types.ReconcileModeObserve}
	_, err := provisionWithLifecycle(t, client, "observe-policy", testSpec{Name: "resource", Value: "desired"}, lifecycle)
	require.NoError(t, err)
	ops.mutate("drifted")

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, "KernelTestResource", "observe-policy", "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.False(t, result.Correcting)
	assert.Empty(t, result.Error)
	assert.Zero(t, ops.snapshot().Updates)

	resourceStatus := status(t, client, "observe-policy")
	assert.Equal(t, types.StatusReady, resourceStatus.Status)
	assert.Equal(t, types.ReconcileModeObserve, resourceStatus.Reconcile)
	driftFree, ok := types.GetCondition(resourceStatus.Conditions, types.ConditionDriftFree)
	require.True(t, ok)
	assert.Equal(t, types.ConditionFalse, driftFree.Status)
}

func TestObserveReconcileKeepsExternallyDeletedResourceReady(t *testing.T) {
	ops := &fakeOperations{}
	client := setupKernelDriver(t, ops)
	_, err := provisionWithLifecycle(t, client, "observe-deleted", testSpec{Name: "resource", Value: "desired"}, types.LifecyclePolicy{Reconcile: types.ReconcileModeObserve})
	require.NoError(t, err)
	before := ops.snapshot()
	ops.remove()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, "KernelTestResource", "observe-deleted", "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.Empty(t, result.Error)
	assert.False(t, result.ReplacementRequired)
	assert.Equal(t, types.StatusReady, status(t, client, "observe-deleted").Status)
	assert.Equal(t, before.Creates, ops.snapshot().Creates, "reconcile must never recreate an externally deleted resource")
}

func TestIgnoreChangesExcludesPeriodicCorrection(t *testing.T) {
	ops := &fakeOperations{}
	client := setupKernelDriver(t, ops)
	lifecycle := types.LifecyclePolicy{Reconcile: types.ReconcileModeAuto, IgnoreChanges: []string{"value"}}
	_, err := provisionWithLifecycle(t, client, "ignored-drift", testSpec{Name: "resource", Value: "desired"}, lifecycle)
	require.NoError(t, err)
	ops.mutate("externally-managed")

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, "KernelTestResource", "ignored-drift", "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.False(t, result.Drift, "ignored differences are not actionable drift")
	assert.False(t, result.Correcting)
	assert.Zero(t, ops.snapshot().Updates)
	assert.Equal(t, []string{"value"}, status(t, client, "ignored-drift").IgnoreChanges)
}

func TestProvisionRejectsNonAlphaLifecycleShape(t *testing.T) {
	ops := &fakeOperations{}
	client := setupKernelDriver(t, ops)

	_, err := provisionWithLifecycle(t, client, "invalid-policy", testSpec{Name: "resource"}, types.LifecyclePolicy{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lifecycle.reconcile")
	assert.Zero(t, ops.snapshot().Creates)

	_, err = provisionWithLifecycle(t, client, "invalid-path", testSpec{Name: "resource"}, types.LifecyclePolicy{Reconcile: types.ReconcileModeAuto, IgnoreChanges: []string{"spec.value"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "relative to spec")
	assert.Zero(t, ops.snapshot().Creates)
}

func TestReconcileFaultsRemainProviderSilentOrVisible(t *testing.T) {
	ops := &fakeOperations{}
	client := setupKernelDriver(t, ops)
	_, err := provision(t, client, "reconcile-faults", testSpec{Name: "resource", Value: "desired"})
	require.NoError(t, err)

	ops.mu.Lock()
	ops.failObserve = true
	ops.mu.Unlock()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, "KernelTestResource", "reconcile-faults", "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "provider read failed")
	assert.Equal(t, types.StatusReady, status(t, client, "reconcile-faults").Status)

	ops.remove()
	result, err = ingress.Object[restate.Void, types.ReconcileResult](client, "KernelTestResource", "reconcile-faults", "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "deleted externally")
	assert.True(t, result.ReplacementRequired)
	assert.Equal(t, types.StatusError, status(t, client, "reconcile-faults").Status)
	assert.Zero(t, ops.snapshot().Updates, "external deletion must not trigger an implicit recreate")
}

func TestDescriptorValidationRejectsImplicitOrIncompleteDefinitions(t *testing.T) {
	valid := testDescriptor(&fakeOperations{})
	tests := []struct {
		name   string
		mutate func(*kernel.Descriptor[testSpec, testOutputs, testObserved])
		want   string
	}{
		{name: "service", mutate: func(d *kernel.Descriptor[testSpec, testOutputs, testObserved]) { d.ServiceName = "" }, want: "service name"},
		{name: "capabilities", mutate: func(d *kernel.Descriptor[testSpec, testOutputs, testObserved]) {
			d.Capabilities = kernel.Capabilities{}
		}, want: "explicitly declared"},
		{name: "operations", mutate: func(d *kernel.Descriptor[testSpec, testOutputs, testObserved]) { d.Operations = nil }, want: "operations"},
		{name: "mapping", mutate: func(d *kernel.Descriptor[testSpec, testOutputs, testObserved]) { d.HasDrift = nil }, want: "lifecycle functions"},
		{name: "field diffs", mutate: func(d *kernel.Descriptor[testSpec, testOutputs, testObserved]) { d.FieldDiffs = nil }, want: "lifecycle functions"},
		{name: "late-init hook", mutate: func(d *kernel.Descriptor[testSpec, testOutputs, testObserved]) {
			d.Capabilities.LateInitialization = true
		}, want: "LateInitialize"},
		{name: "readiness predicate", mutate: func(d *kernel.Descriptor[testSpec, testOutputs, testObserved]) {
			d.Capabilities.Readiness = true
		}, want: "CheckReadiness"},
		{name: "undeclared readiness predicate", mutate: func(d *kernel.Descriptor[testSpec, testOutputs, testObserved]) {
			d.CheckReadiness = func(testObserved) kernel.ReadinessResult {
				return kernel.ReadinessResult{Phase: kernel.ReadinessReady}
			}
		}, want: "readiness capability"},
		{name: "pending convergence without readiness", mutate: func(d *kernel.Descriptor[testSpec, testOutputs, testObserved]) {
			d.Capabilities.ConvergeWhilePending = true
		}, want: "requires the readiness capability"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			descriptor := valid
			tt.mutate(&descriptor)
			_, err := kernel.New(descriptor)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want, fmt.Sprintf("unexpected validation error: %v", err))
		})
	}
}

func TestReadinessKeepsProvisionedIdentityPendingUntilReconcile(t *testing.T) {
	ops := &fakeOperations{}
	descriptor := testDescriptor(ops)
	descriptor.Capabilities.Readiness = true
	descriptor.CheckReadiness = func(observed testObserved) kernel.ReadinessResult {
		if observed.Ready {
			return kernel.ReadinessResult{Phase: kernel.ReadinessReady}
		}
		return kernel.ReadinessResult{Phase: kernel.ReadinessPending, Message: "provider is still starting"}
	}
	driver := kernel.MustNew(descriptor)
	client := restatetest.Start(t, restate.Reflect(driver), restate.Reflect(driftSink{})).Ingress()

	outputs, err := provision(t, client, "readiness", testSpec{Name: "resource", Value: "waiting"})
	require.NoError(t, err)
	assert.Equal(t, "provider-resource", outputs.ID, "pending state must retain provider identity")
	assert.Equal(t, types.StatusPending, status(t, client, "readiness").Status)
	assert.Zero(t, ops.snapshot().Updates, "pending resources must not be converged before readiness")

	ops.setReady()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, "KernelTestResource", "readiness", "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.False(t, result.Drift)
	assert.Equal(t, types.StatusReady, status(t, client, "readiness").Status)
}

func readinessFromObserved(observed testObserved) kernel.ReadinessResult {
	if observed.Failed {
		return kernel.ReadinessResult{Phase: kernel.ReadinessFailed, Message: "provider entered FAILED state"}
	}
	if observed.Ready {
		return kernel.ReadinessResult{Phase: kernel.ReadinessReady}
	}
	return kernel.ReadinessResult{Phase: kernel.ReadinessPending, Message: "provider is still starting"}
}

func TestReadinessFailedPersistsErrorAndUsesTerminal409(t *testing.T) {
	t.Run("provision", func(t *testing.T) {
		ops := &fakeOperations{}
		descriptor := testDescriptor(ops)
		descriptor.Capabilities.Readiness = true
		descriptor.CheckReadiness = readinessFromObserved
		client := restatetest.Start(t, restate.Reflect(kernel.MustNew(descriptor)), restate.Reflect(driftSink{})).Ingress()

		ops.mu.Lock()
		ops.observed = testObserved{ID: "provider-failed", Name: "resource", Value: "visible", Failed: true}
		ops.mu.Unlock()
		_, err := provision(t, client, "failed-provision", testSpec{Name: "resource", Value: "visible"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "409")
		assert.Contains(t, err.Error(), "provider entered FAILED state")
		got := status(t, client, "failed-provision")
		assert.Equal(t, types.StatusError, got.Status)
		assert.Contains(t, got.Error, "provider entered FAILED state")
	})

	t.Run("import", func(t *testing.T) {
		ops := &fakeOperations{observed: testObserved{ID: "provider-failed", Name: "resource", Value: "visible", Failed: true}}
		descriptor := testDescriptor(ops)
		descriptor.Capabilities.Readiness = true
		descriptor.CheckReadiness = readinessFromObserved
		client := restatetest.Start(t, restate.Reflect(kernel.MustNew(descriptor)), restate.Reflect(driftSink{})).Ingress()

		_, err := ingress.Object[types.ImportRef, testOutputs](client, "KernelTestResource", "failed-import", "Import").Request(
			t.Context(), types.ImportRef{ResourceID: "provider-failed"},
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "409")
		assert.Equal(t, types.StatusError, status(t, client, "failed-import").Status)
	})
}

func TestPendingReadinessFailureDuringReconcileIsVisibilityOnly(t *testing.T) {
	ops := &fakeOperations{}
	descriptor := testDescriptor(ops)
	descriptor.Capabilities.Readiness = true
	descriptor.CheckReadiness = readinessFromObserved
	client := restatetest.Start(t, restate.Reflect(kernel.MustNew(descriptor)), restate.Reflect(driftSink{})).Ingress()
	key := "pending-then-failed"

	_, err := provision(t, client, key, testSpec{Name: "resource", Value: "visible"})
	require.NoError(t, err)
	assert.Equal(t, types.StatusPending, status(t, client, key).Status)
	ops.mu.Lock()
	ops.observed.Failed = true
	ops.mu.Unlock()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, "KernelTestResource", key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err, "Reconcile exposes provider failure in its result instead of failing the handler")
	assert.Contains(t, result.Error, "provider entered FAILED state")
	assert.Equal(t, types.StatusError, status(t, client, key).Status)
}

func TestConvergeWhilePendingIsUnconditionalForManagedAndSilentForObserved(t *testing.T) {
	newDescriptor := func(ops *fakeOperations) kernel.Descriptor[testSpec, testOutputs, testObserved] {
		descriptor := testDescriptor(ops)
		descriptor.Capabilities.Readiness = true
		descriptor.Capabilities.ConvergeWhilePending = true
		descriptor.CheckReadiness = func(testObserved) kernel.ReadinessResult {
			return kernel.ReadinessResult{Phase: kernel.ReadinessPending, Message: "awaiting external acceptance"}
		}
		return descriptor
	}

	t.Run("managed Provision and Reconcile", func(t *testing.T) {
		ops := &fakeOperations{}
		client := restatetest.Start(t, restate.Reflect(kernel.MustNew(newDescriptor(ops))), restate.Reflect(driftSink{})).Ingress()
		key := "managed-pending"
		_, err := provision(t, client, key, testSpec{Name: "resource", Value: "already-matching"})
		require.NoError(t, err)
		assert.Equal(t, 1, ops.snapshot().Updates, "capability invokes Converge even though ordinary HasDrift is false")
		assert.Equal(t, types.StatusPending, status(t, client, key).Status)

		_, err = ingress.Object[restate.Void, types.ReconcileResult](client, "KernelTestResource", key, "Reconcile").Request(t.Context(), restate.Void{})
		require.NoError(t, err)
		assert.Equal(t, 2, ops.snapshot().Updates, "managed pending reconciliation invokes Converge unconditionally")
	})

	t.Run("managed Provision conflict retains last accepted desired", func(t *testing.T) {
		ops := &fakeOperations{}
		client := restatetest.Start(t, restate.Reflect(kernel.MustNew(newDescriptor(ops))), restate.Reflect(driftSink{})).Ingress()
		key := "managed-pending-conflict"
		accepted := testSpec{Name: "resource", Value: "accepted"}
		_, err := provision(t, client, key, accepted)
		require.NoError(t, err)

		ops.failConverge(restate.TerminalError(errors.New("pending provider identity is immutable"), 409))
		_, err = provision(t, client, key, testSpec{Name: "resource", Value: "rejected"})
		require.Error(t, err)

		stored, getErr := ingress.Object[restate.Void, testSpec](client, "KernelTestResource", key, "GetInputs").Request(t.Context(), restate.Void{})
		require.NoError(t, getErr)
		assert.Equal(t, accepted, stored, "a terminal 409 from pending Converge must not replace the last accepted desired contract")
		assert.Equal(t, types.StatusError, status(t, client, key).Status)
	})

	t.Run("observed Reconcile", func(t *testing.T) {
		ops := &fakeOperations{observed: testObserved{ID: "provider-pending", Name: "resource", Value: "current"}}
		client := restatetest.Start(t, restate.Reflect(kernel.MustNew(newDescriptor(ops))), restate.Reflect(driftSink{})).Ingress()
		_, err := ingress.Object[types.ImportRef, testOutputs](client, "KernelTestResource", "observed-pending", "Import").Request(
			t.Context(), types.ImportRef{ResourceID: "provider-pending"},
		)
		require.NoError(t, err)
		assert.Equal(t, types.StatusPending, status(t, client, "observed-pending").Status)

		_, err = ingress.Object[restate.Void, types.ReconcileResult](client, "KernelTestResource", "observed-pending", "Reconcile").Request(t.Context(), restate.Void{})
		require.NoError(t, err)
		assert.Zero(t, ops.snapshot().Updates, "observed pending resources remain provider-silent")
	})
}

func TestDeleteProvisionedPendingResourceCallsProvider(t *testing.T) {
	ops := &fakeOperations{}
	descriptor := testDescriptor(ops)
	descriptor.Capabilities.Readiness = true
	descriptor.CheckReadiness = func(testObserved) kernel.ReadinessResult {
		return kernel.ReadinessResult{Phase: kernel.ReadinessPending}
	}
	driver := kernel.MustNew(descriptor)
	client := restatetest.Start(t, restate.Reflect(driver), restate.Reflect(driftSink{})).Ingress()

	_, err := provision(t, client, "pending-delete", testSpec{Name: "resource", Value: "waiting"})
	require.NoError(t, err)
	assert.Equal(t, types.StatusPending, status(t, client, "pending-delete").Status)
	_, err = ingress.Object[restate.Void, restate.Void](client, "KernelTestResource", "pending-delete", "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, 1, ops.snapshot().Deletes)
	assert.Equal(t, types.StatusDeleted, status(t, client, "pending-delete").Status)
}

func TestLateInitializationPersistsProviderDefaultInDesiredState(t *testing.T) {
	ops := &fakeOperations{createDefault: "provider-default"}
	descriptor := testDescriptor(ops)
	descriptor.Capabilities.LateInitialization = true
	descriptor.LateInitialize = func(desired testSpec, observed testObserved) (testSpec, bool) {
		if desired.Value != "" {
			return desired, false
		}
		desired.Value = observed.Value
		return desired, true
	}
	driver := kernel.MustNew(descriptor)
	client := restatetest.Start(t, restate.Reflect(driver), restate.Reflect(driftSink{})).Ingress()

	_, err := provision(t, client, "late-init", testSpec{Name: "late-init"})
	require.NoError(t, err)
	inputs, err := ingress.Object[restate.Void, testSpec](client, "KernelTestResource", "late-init", "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, "provider-default", inputs.Value)
	initialized, ok := types.GetCondition(status(t, client, "late-init").Conditions, types.ConditionInitialized)
	require.True(t, ok)
	assert.Equal(t, types.ConditionTrue, initialized.Status)
	assert.Equal(t, types.ReasonLateInitialized, initialized.Reason)
	assert.Contains(t, initialized.Message, "adopted")
}
