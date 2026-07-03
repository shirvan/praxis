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
	provisions atomic.Int64
	// inFlight/maxInFlight observe dispatch concurrency across object keys.
	inFlight    atomic.Int64
	maxInFlight atomic.Int64
	// workDelay is how long each provision "works", to create overlap windows.
	workDelay time.Duration
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
		time.Sleep(f.workDelay)
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

func (f *fakeS3Driver) resetObservations() {
	f.inFlight.Store(0)
	f.maxInFlight.Store(0)
	f.provisions.Store(0)
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

	fake := &fakeS3Driver{workDelay: 400 * time.Millisecond}
	env := restatetest.Start(t,
		restate.Reflect(authservice.NewAuthService(authservice.LoadBootstrapFromEnv())),
		restate.Reflect(cmdService),
		restate.Reflect(applyWorkflow),
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
