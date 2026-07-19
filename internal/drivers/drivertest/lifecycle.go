// Package drivertest provides reusable black-box conformance checks for Praxis
// resource drivers. It is intended for driver tests, not production code.
package drivertest

import (
	"encoding/json"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

// ProviderSnapshot is the provider-call surface relevant to the common
// lifecycle contract. Driver fixtures aggregate resource-specific API methods
// into these four categories.
type ProviderSnapshot struct {
	Creates int
	Reads   int
	Updates int
	Deletes int
}

// CoreLifecycleFixture describes one create-capable driver fixture. The
// provider double must be stateful so two Provision calls observe the same
// provider resource.
type CoreLifecycleFixture[Spec, Outputs any] struct {
	Client      *ingress.Client
	ServiceName string
	Key         string
	Spec        Spec
	Snapshot    func() ProviderSnapshot

	// AssertInputs may account for documented normalization or late defaults.
	// When nil, the stored inputs must exactly equal Spec.
	AssertInputs func(t *testing.T, inputs Spec)

	// AllowNoopUpdates permits resources whose provider contract requires a
	// convergent write on every Provision even when the desired state matches.
	AllowNoopUpdates bool
}

// ObservedImportFixture describes a provider resource that already exists and
// can be adopted through the driver's Import handler.
type ObservedImportFixture[Outputs any] struct {
	Client      *ingress.Client
	ServiceName string
	Key         string
	Ref         types.ImportRef
	Snapshot    func() ProviderSnapshot
}

// ProvisionRequest encodes a typed spec in the single alpha Provision
// envelope used by every driver. Tests use this helper so they exercise the
// production wire contract without repeating JSON plumbing.
func ProvisionRequest(t testing.TB, spec any) types.ProvisionRequest {
	t.Helper()
	encoded, err := json.Marshal(spec)
	require.NoError(t, err)
	return types.ProvisionRequest{
		Spec:      encoded,
		Lifecycle: types.NormalizeLifecyclePolicy(nil),
	}
}

// RunCoreLifecycle exercises invariants shared by every managed resource:
// create-or-converge idempotency, readable atomic state, retained deletion
// tombstones, double-delete idempotency, and no reconciliation after deletion.
func RunCoreLifecycle[Spec, Outputs any](t *testing.T, fixture CoreLifecycleFixture[Spec, Outputs]) {
	t.Helper()
	require.NotNil(t, fixture.Client)
	require.NotEmpty(t, fixture.ServiceName)
	require.NotEmpty(t, fixture.Key)
	require.NotNil(t, fixture.Snapshot)

	beforeProvision := fixture.Snapshot()
	encodedSpec, err := json.Marshal(fixture.Spec)
	require.NoError(t, err)
	request := types.ProvisionRequest{
		Spec: encodedSpec, Lifecycle: types.LifecyclePolicy{Reconcile: types.ReconcileModeAuto},
	}
	firstOutputs, err := ingress.Object[types.ProvisionRequest, Outputs](
		fixture.Client, fixture.ServiceName, fixture.Key, "Provision",
	).Request(t.Context(), request)
	require.NoError(t, err)
	afterProvision := fixture.Snapshot()
	require.Greater(t, afterProvision.Creates, beforeProvision.Creates, "fixture must exercise the create path")

	status := getStatus(t, fixture)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Positive(t, status.Generation)
	assert.Empty(t, status.Error)

	storedOutputs, err := ingress.Object[restate.Void, Outputs](
		fixture.Client, fixture.ServiceName, fixture.Key, "GetOutputs",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, firstOutputs, storedOutputs, "Provision and GetOutputs must expose the same committed outputs")

	storedInputs, err := ingress.Object[restate.Void, Spec](
		fixture.Client, fixture.ServiceName, fixture.Key, "GetInputs",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	if fixture.AssertInputs == nil {
		assert.Equal(t, fixture.Spec, storedInputs)
	} else {
		fixture.AssertInputs(t, storedInputs)
	}

	beforeSecondProvision := fixture.Snapshot()
	secondOutputs, err := ingress.Object[types.ProvisionRequest, Outputs](
		fixture.Client, fixture.ServiceName, fixture.Key, "Provision",
	).Request(t.Context(), request)
	require.NoError(t, err)
	afterSecondProvision := fixture.Snapshot()
	assert.Equal(t, beforeSecondProvision.Creates, afterSecondProvision.Creates, "identical Provision must not create a second provider resource")
	if !fixture.AllowNoopUpdates {
		assert.Equal(t, beforeSecondProvision.Updates, afterSecondProvision.Updates, "identical Provision must not issue provider mutations")
	}
	assert.Equal(t, beforeSecondProvision.Deletes, afterSecondProvision.Deletes, "identical Provision must not delete provider state")
	assert.Equal(t, firstOutputs, secondOutputs, "identical Provision must preserve resource identity and outputs")

	beforeDelete := fixture.Snapshot()
	_, err = ingress.Object[restate.Void, restate.Void](
		fixture.Client, fixture.ServiceName, fixture.Key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	afterDelete := fixture.Snapshot()
	require.Greater(t, afterDelete.Deletes, beforeDelete.Deletes, "fixture must exercise the provider delete path")
	assert.Equal(t, types.StatusDeleted, getStatus(t, fixture).Status)

	beforeSecondDelete := fixture.Snapshot()
	_, err = ingress.Object[restate.Void, restate.Void](
		fixture.Client, fixture.ServiceName, fixture.Key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, beforeSecondDelete, fixture.Snapshot(), "a retained tombstone must make the second Delete provider-silent")
	assert.Equal(t, types.StatusDeleted, getStatus(t, fixture).Status)

	beforeReconcile := fixture.Snapshot()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		fixture.Client, fixture.ServiceName, fixture.Key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ReconcileResult{}, result)
	assert.Equal(t, beforeReconcile, fixture.Snapshot(), "Reconcile must not inspect or mutate a deleted provider resource")
	assert.Equal(t, types.StatusDeleted, getStatus(t, fixture).Status)
}

// RunObservedImportLifecycle verifies the read-only ownership boundary:
// default Import adopts as Observed, Delete is rejected before any provider
// call, and Reconcile may inspect but never mutate the provider resource.
func RunObservedImportLifecycle[Outputs any](t *testing.T, fixture ObservedImportFixture[Outputs]) {
	t.Helper()
	require.NotNil(t, fixture.Client)
	require.NotEmpty(t, fixture.ServiceName)
	require.NotEmpty(t, fixture.Key)
	require.NotEmpty(t, fixture.Ref.ResourceID)
	require.Empty(t, fixture.Ref.Mode, "fixture must exercise the default Observed import mode")
	require.NotNil(t, fixture.Snapshot)

	beforeImport := fixture.Snapshot()
	importedOutputs, err := ingress.Object[types.ImportRef, Outputs](
		fixture.Client, fixture.ServiceName, fixture.Key, "Import",
	).Request(t.Context(), fixture.Ref)
	require.NoError(t, err)
	afterImport := fixture.Snapshot()
	assert.Equal(t, beforeImport.Creates, afterImport.Creates, "Import must not create a provider resource")
	assert.Equal(t, beforeImport.Updates, afterImport.Updates, "Import must not mutate a provider resource")
	assert.Equal(t, beforeImport.Deletes, afterImport.Deletes, "Import must not delete a provider resource")
	assert.Greater(t, afterImport.Reads, beforeImport.Reads, "Import must observe provider state")

	status := getObservedStatus(t, fixture)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeObserved, status.Mode)
	assert.Positive(t, status.Generation)
	assert.Empty(t, status.Error)

	storedOutputs, err := ingress.Object[restate.Void, Outputs](
		fixture.Client, fixture.ServiceName, fixture.Key, "GetOutputs",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, importedOutputs, storedOutputs)

	beforeDelete := fixture.Snapshot()
	_, err = ingress.Object[restate.Void, restate.Void](
		fixture.Client, fixture.ServiceName, fixture.Key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.Error(t, err, "Observed resources must reject Delete")
	assert.Equal(t, beforeDelete, fixture.Snapshot(), "Observed Delete must be rejected before any provider call")
	status = getObservedStatus(t, fixture)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeObserved, status.Mode)

	beforeReconcile := fixture.Snapshot()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		fixture.Client, fixture.ServiceName, fixture.Key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	afterReconcile := fixture.Snapshot()
	assert.False(t, result.Drift, "immediate Reconcile after Import must not report phantom drift")
	assert.False(t, result.Correcting, "Observed Reconcile must never correct provider state")
	assert.Equal(t, beforeReconcile.Creates, afterReconcile.Creates)
	assert.Equal(t, beforeReconcile.Updates, afterReconcile.Updates)
	assert.Equal(t, beforeReconcile.Deletes, afterReconcile.Deletes)
	assert.Greater(t, afterReconcile.Reads, beforeReconcile.Reads, "Observed Reconcile must refresh provider state")
	status = getObservedStatus(t, fixture)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func getStatus[Spec, Outputs any](t *testing.T, fixture CoreLifecycleFixture[Spec, Outputs]) types.StatusResponse {
	t.Helper()
	status, err := ingress.Object[restate.Void, types.StatusResponse](
		fixture.Client, fixture.ServiceName, fixture.Key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return status
}

func getObservedStatus[Outputs any](t *testing.T, fixture ObservedImportFixture[Outputs]) types.StatusResponse {
	t.Helper()
	status, err := ingress.Object[restate.Void, types.StatusResponse](
		fixture.Client, fixture.ServiceName, fixture.Key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return status
}
