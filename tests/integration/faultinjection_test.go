//go:build integration

// Fault-injection tests for the deployment workflow's retry and parallelism
// behavior. A fake S3Bucket virtual object stands in for the real driver: the
// orchestrator dispatches by service name, so binding the fake instead of the
// real driver lets tests inject retryable failures and observe dispatch
// concurrency without Moto being able to produce those conditions on demand.
package integration

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/command"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/internal/core/registry"
	"github.com/shirvan/praxis/internal/drivers/s3"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

// fakeS3Driver impersonates the S3Bucket driver service. Failure decisions and
// concurrency observations happen inside restate.Run so journal replay cannot
// double-count them.
type fakeS3Driver struct {
	// failuresRemaining injects one retryable failure per decrement while > 0.
	failuresRemaining atomic.Int64
	// provisions counts Provision side-effect executions (not replays).
	provisions           atomic.Int64
	provisionCompletions atomic.Int64
	// deletes and deleteCompletions distinguish a dispatched delete from one
	// that ran to successful completion after the orchestrator stopped waiting.
	deletes           atomic.Int64
	deleteCompletions atomic.Int64
	// inFlight/maxInFlight observe dispatch concurrency across object keys.
	inFlight    atomic.Int64
	maxInFlight atomic.Int64
	// Delay values are stored as nanoseconds so tests can change the next
	// operation's behavior without racing a concurrently executing handler.
	provisionDelayNanos atomic.Int64
	deleteDelayNanos    atomic.Int64
}

func (f *fakeS3Driver) ServiceName() string { return "S3Bucket" }

func (f *fakeS3Driver) Provision(ctx restate.ObjectContext, spec s3.S3BucketSpec) (s3.S3BucketOutputs, error) {
	shouldFail, err := restate.Run(ctx, func(rc restate.RunContext) (bool, error) {
		f.provisions.Add(1)
		cur := f.inFlight.Add(1)
		defer f.inFlight.Add(-1)
		for {
			max := f.maxInFlight.Load()
			if cur <= max || f.maxInFlight.CompareAndSwap(max, cur) {
				break
			}
		}
		delay := time.NewTimer(time.Duration(f.provisionDelayNanos.Load()))
		defer delay.Stop()
		select {
		case <-delay.C:
		case <-rc.Done():
			return false, rc.Err()
		}
		f.provisionCompletions.Add(1)
		return f.failuresRemaining.Add(-1) >= 0, nil
	})
	if err != nil {
		return s3.S3BucketOutputs{}, err
	}
	if shouldFail {
		// Code 429 + a "retryable" message is the provider layer's contract
		// for resource-level retryable failures (decodeRetryableInvocationError).
		return s3.S3BucketOutputs{}, restate.TerminalError(errors.New("retryable: injected fault"), 429)
	}
	return s3.S3BucketOutputs{
		ARN:        "arn:aws:s3:::" + spec.BucketName,
		BucketName: spec.BucketName,
		Region:     spec.Region,
	}, nil
}

// Delete deliberately performs its observable work inside restate.Run, just
// like production drivers. Timeout tests use its counters to prove that the
// durable invocation was not cancelled when the orchestrator's wait expired.
func (f *fakeS3Driver) Delete(ctx restate.ObjectContext) error {
	_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		f.deletes.Add(1)
		delay := time.NewTimer(time.Duration(f.deleteDelayNanos.Load()))
		defer delay.Stop()
		select {
		case <-delay.C:
		case <-rc.Done():
			return restate.Void{}, rc.Err()
		}
		f.deleteCompletions.Add(1)
		return restate.Void{}, nil
	})
	return err
}

// PreDelete satisfies the S3 adapter's finalizer hook. The timeout scenarios
// intentionally isolate the durable Delete invocation, so cleanup itself is a
// no-op in this fake service.
func (f *fakeS3Driver) PreDelete(ctx restate.ObjectContext) error {
	return nil
}

func (f *fakeS3Driver) resetObservations() {
	f.inFlight.Store(0)
	f.maxInFlight.Store(0)
	f.provisions.Store(0)
	f.provisionCompletions.Store(0)
	f.deletes.Store(0)
	f.deleteCompletions.Store(0)
}

// setupFaultStack boots the core Praxis stack with the fake S3 driver bound in
// place of the real one.
func setupFaultStack(t *testing.T) (*ingress.Client, *fakeS3Driver) {
	t.Helper()
	configureLocalAccount(t)

	authClient := authservice.NewAuthClient()
	providers := provider.NewRegistry(authClient)
	absSchemaDir, err := filepath.Abs("../../schemas")
	require.NoError(t, err)
	cmdService := command.NewPraxisCommandService(config.Config{SchemaDir: absSchemaDir}, authClient, providers)
	applyWorkflow := orchestrator.NewDeploymentWorkflow(providers)
	deleteWorkflow := orchestrator.NewDeploymentDeleteWorkflow(providers)

	fake := &fakeS3Driver{}
	fake.provisionDelayNanos.Store(int64(400 * time.Millisecond))
	env := restatetest.Start(t,
		restate.Reflect(authservice.NewAuthService(authservice.LoadBootstrapFromEnv())),
		restate.Reflect(cmdService),
		restate.Reflect(applyWorkflow),
		restate.Reflect(deleteWorkflow),
		restate.Reflect(orchestrator.DeploymentStateObj{}),
		restate.Reflect(orchestrator.DeploymentIndex{}),
		restate.Reflect(orchestrator.ResourceIndex{}),
		restate.Reflect(orchestrator.NewEventBus(absSchemaDir)),
		restate.Reflect(orchestrator.DeploymentEventStore{}),
		restate.Reflect(orchestrator.ResourceEventOwnerObj{}),
		restate.Reflect(orchestrator.ResourceEventBridge{}),
		restate.Reflect(orchestrator.SinkRouter{}),
		restate.Reflect(orchestrator.NewNotificationSinkConfig(absSchemaDir)),
		restate.Reflect(registry.PolicyRegistry{}),
		restate.Reflect(fake),
	)
	return env.Ingress(), fake
}

func bucketTemplateWithTimeouts(bucketName, createTimeout, deleteTimeout string) string {
	return fmt.Sprintf(`
resources: bucket: {
	apiVersion: "praxis.io/v1"
	kind:       "S3Bucket"
	metadata: name: %q
	spec: region: "us-east-1"
	lifecycle: timeouts: {
		create: %q
		delete: %q
	}
}
`, bucketName, createTimeout, deleteTimeout)
}

func requireTimedOutResourceRemainsUnknown(t *testing.T, state *orchestrator.DeploymentState, resourceName string) {
	t.Helper()
	require.Equal(t, types.DeploymentFailed, state.Status)

	resource := state.Resources[resourceName]
	require.NotNil(t, resource)
	assert.Equal(t, types.DeploymentResourceError, resource.Status)
	assert.Contains(t, resource.Error, "outcome is unknown")
	assert.Contains(t, resource.Error, "invocation continues")

	for _, conditionType := range []string{types.ConditionProvisioned, types.ConditionReady} {
		condition, ok := types.GetCondition(resource.Conditions, conditionType)
		require.True(t, ok, "resource must record the %s condition", conditionType)
		assert.Equal(t, types.ConditionUnknown, condition.Status)
		assert.Equal(t, types.ReasonTimedOut, condition.Reason)
	}
}

func nBucketTemplate(prefix string, n int) string {
	var b strings.Builder
	b.WriteString("resources: {\n")
	for i := range n {
		fmt.Fprintf(&b, `
	bucket%d: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: { name: "%s-%d" }
		spec: { region: "us-east-1" }
	}
`, i, prefix, i)
	}
	b.WriteString("}\n")
	return b.String()
}

func applyViaIngress(t *testing.T, client *ingress.Client, deployKey, template string, maxParallelism int, maxRetries *int) {
	t.Helper()
	_, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		client, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:       template,
		DeploymentKey:  deployKey,
		Variables:      accountVariables(),
		MaxParallelism: maxParallelism,
		MaxRetries:     maxRetries,
	})
	require.NoError(t, err)
}

// TestWorkflow_RetryableFailureRecovers: one injected retryable failure, then
// success — the workflow must retry the resource and complete the deployment.
func TestWorkflow_RetryableFailureRecovers(t *testing.T) {
	client, fake := setupFaultStack(t)
	fake.failuresRemaining.Store(1)
	deployKey := "test-retry-recovers-" + uniqueName(t, "dep")
	maxRetries := 2

	applyViaIngress(t, client, deployKey, nBucketTemplate(uniqueName(t, "rr"), 1), 0, &maxRetries)

	state := pollDeploymentState(t, client, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete, types.DeploymentFailed}, 120*time.Second)
	require.Equal(t, types.DeploymentComplete, state.Status,
		"one retryable failure within the retry budget must not fail the deployment — error: %v", state.Error)
	assert.Equal(t, int64(2), fake.provisions.Load(),
		"the resource should have been provisioned exactly twice (failure + retry)")
}

// TestWorkflow_RetryLimitExhausted: failures never stop — the workflow must
// give up after MaxRetries and mark the deployment Failed with the
// retry-limit message.
func TestWorkflow_RetryLimitExhausted(t *testing.T) {
	client, fake := setupFaultStack(t)
	fake.failuresRemaining.Store(1_000_000)
	deployKey := "test-retry-exhausted-" + uniqueName(t, "dep")
	maxRetries := 1

	applyViaIngress(t, client, deployKey, nBucketTemplate(uniqueName(t, "re"), 1), 0, &maxRetries)

	state := pollDeploymentState(t, client, deployKey,
		[]types.DeploymentStatus{types.DeploymentFailed, types.DeploymentComplete}, 120*time.Second)
	require.Equal(t, types.DeploymentFailed, state.Status, "exhausted retries must fail the deployment")
	assert.Contains(t, state.Error, "retry limit exceeded")
	assert.Equal(t, int64(2), fake.provisions.Load(),
		"initial attempt plus exactly MaxRetries retries")
}

// TestWorkflow_MaxParallelismLimitsDispatch: with MaxParallelism=1 four
// independent resources must provision strictly sequentially; unlimited
// dispatch of the same shape must overlap (proving the probe can detect
// concurrency at all).
func TestWorkflow_MaxParallelismLimitsDispatch(t *testing.T) {
	client, fake := setupFaultStack(t)

	deployKey := "test-parallel-1-" + uniqueName(t, "dep")
	applyViaIngress(t, client, deployKey, nBucketTemplate(uniqueName(t, "p1"), 4), 1, nil)
	state := pollDeploymentState(t, client, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete}, 120*time.Second)
	require.Equal(t, types.DeploymentComplete, state.Status)
	assert.Equal(t, int64(1), fake.maxInFlight.Load(),
		"MaxParallelism=1 must serialize resource dispatch")

	fake.resetObservations()

	deployKey = "test-parallel-n-" + uniqueName(t, "dep")
	applyViaIngress(t, client, deployKey, nBucketTemplate(uniqueName(t, "pn"), 4), 0, nil)
	state = pollDeploymentState(t, client, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete}, 120*time.Second)
	require.Equal(t, types.DeploymentComplete, state.Status)
	assert.Greater(t, fake.maxInFlight.Load(), int64(1),
		"unlimited parallelism should overlap independent provisions; if this fails the concurrency probe is broken")
}

// TestWorkflow_ProvisionTimeoutLeavesLateSuccessUnknown proves the timeout is
// an observation deadline, not cancellation. The fake driver completes after
// the deadline, but that late success cannot turn the failed generation green.
func TestWorkflow_ProvisionTimeoutLeavesLateSuccessUnknown(t *testing.T) {
	client, fake := setupFaultStack(t)
	fake.provisionDelayNanos.Store(int64(1 * time.Second))

	deployKey := "test-provision-timeout-" + uniqueName(t, "dep")
	applyViaIngress(t, client, deployKey,
		bucketTemplateWithTimeouts(uniqueName(t, "timeout"), "200ms", "5s"), 0, nil)

	state := pollDeploymentState(t, client, deployKey,
		[]types.DeploymentStatus{types.DeploymentFailed, types.DeploymentComplete}, 120*time.Second)
	requireTimedOutResourceRemainsUnknown(t, state, "bucket")

	// The completion counter can only advance inside the durable driver call.
	// Reaching one after the workflow failed proves Praxis did not cancel it.
	waitForCounter(t, &fake.provisionCompletions, 1, 10*time.Second, "late provision completion")
	lateState := pollDeploymentState(t, client, deployKey,
		[]types.DeploymentStatus{types.DeploymentFailed, types.DeploymentComplete}, 10*time.Second)
	requireTimedOutResourceRemainsUnknown(t, lateState, "bucket")
	assert.Empty(t, lateState.Outputs["bucket"], "late outputs must not be accepted by the failed generation")
}

// TestWorkflow_ReplaceDeleteTimeoutLeavesLateSuccessUnknown covers the other
// dangerous edge: replacement deletion can succeed after Praxis times out.
// The old resource may be gone, but the generation must remain non-green and
// must not dispatch the replacement provision based on an abandoned future.
func TestWorkflow_ReplaceDeleteTimeoutLeavesLateSuccessUnknown(t *testing.T) {
	client, fake := setupFaultStack(t)
	deployKey := "test-replace-delete-timeout-" + uniqueName(t, "dep")
	bucketName := uniqueName(t, "replace-timeout")
	template := bucketTemplateWithTimeouts(bucketName, "5s", "200ms")

	applyViaIngress(t, client, deployKey, template, 0, nil)
	initial := pollDeploymentState(t, client, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete, types.DeploymentFailed}, 120*time.Second)
	require.Equal(t, types.DeploymentComplete, initial.Status, "fixture apply must succeed: %s", initial.Error)
	require.Equal(t, int64(1), fake.provisions.Load())

	fake.deleteDelayNanos.Store(int64(1 * time.Second))
	_, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		client, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      template,
		DeploymentKey: deployKey,
		Variables:     accountVariables(),
		Replace:       []string{"bucket"},
	})
	require.NoError(t, err)

	timedOut := pollDeploymentState(t, client, deployKey,
		[]types.DeploymentStatus{types.DeploymentFailed, types.DeploymentComplete}, 120*time.Second)
	requireTimedOutResourceRemainsUnknown(t, timedOut, "bucket")
	assert.Contains(t, timedOut.Resources["bucket"].Error, "force-replace delete")

	waitForCounter(t, &fake.deleteCompletions, 1, 10*time.Second, "late replace delete completion")
	lateState := pollDeploymentState(t, client, deployKey,
		[]types.DeploymentStatus{types.DeploymentFailed, types.DeploymentComplete}, 10*time.Second)
	requireTimedOutResourceRemainsUnknown(t, lateState, "bucket")
	assert.Equal(t, int64(1), fake.deletes.Load())
	assert.Equal(t, int64(1), fake.provisions.Load(), "replacement provision must not run after an abandoned delete future")
}

// TestDeleteWorkflow_TimeoutLeavesLateSuccessUnknown exercises the dedicated
// reverse-DAG delete workflow. A late successful delete changes the fake
// provider, but it cannot rewrite the already failed Praxis generation.
func TestDeleteWorkflow_TimeoutLeavesLateSuccessUnknown(t *testing.T) {
	client, fake := setupFaultStack(t)
	deployKey := "test-delete-timeout-" + uniqueName(t, "dep")
	template := bucketTemplateWithTimeouts(uniqueName(t, "delete-timeout"), "5s", "200ms")

	applyViaIngress(t, client, deployKey, template, 0, nil)
	initial := pollDeploymentState(t, client, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete, types.DeploymentFailed}, 120*time.Second)
	require.Equal(t, types.DeploymentComplete, initial.Status, "fixture apply must succeed: %s", initial.Error)

	fake.deleteDelayNanos.Store(int64(1 * time.Second))
	_, err := ingress.Service[command.DeleteDeploymentRequest, command.DeleteDeploymentResponse](
		client, "PraxisCommandService", "DeleteDeployment",
	).Request(t.Context(), command.DeleteDeploymentRequest{DeploymentKey: deployKey})
	require.NoError(t, err)

	timedOut := pollDeploymentState(t, client, deployKey,
		[]types.DeploymentStatus{types.DeploymentFailed, types.DeploymentDeleted}, 120*time.Second)
	requireTimedOutResourceRemainsUnknown(t, timedOut, "bucket")
	assert.Contains(t, timedOut.Resources["bucket"].Error, "resource delete")

	waitForCounter(t, &fake.deleteCompletions, 1, 10*time.Second, "late delete completion")
	lateState := pollDeploymentState(t, client, deployKey,
		[]types.DeploymentStatus{types.DeploymentFailed, types.DeploymentDeleted}, 10*time.Second)
	requireTimedOutResourceRemainsUnknown(t, lateState, "bucket")
	assert.Equal(t, int64(1), fake.deletes.Load())
}
