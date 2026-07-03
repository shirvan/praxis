//go:build integration

// Crash-resume tests: Praxis's core durability promise is that Restate
// journals every step, so a crashed server resumes work without repeating
// side effects. These tests restart the Restate container mid-invocation and
// assert both halves of that promise — exactly-once side effects (journal
// replay) and eventual completion (recovery).
package integration

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/core/command"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

// crashProbe is a minimal virtual object whose Run handler executes a
// journaled side effect, suspends on a durable timer, then executes a second
// journaled side effect. Killing Restate while the invocation is suspended on
// the timer exercises the exact recovery path Praxis drivers rely on.
type crashProbe struct {
	step1 *atomic.Int64
	step2 *atomic.Int64
}

func (p *crashProbe) ServiceName() string { return "CrashProbe" }

func (p *crashProbe) Run(ctx restate.ObjectContext) (string, error) {
	_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		p.step1.Add(1)
		return restate.Void{}, nil
	})
	if err != nil {
		return "", err
	}
	if err := restate.Sleep(ctx, 4*time.Second); err != nil {
		return "", err
	}
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		p.step2.Add(1)
		return restate.Void{}, nil
	})
	if err != nil {
		return "", err
	}
	return "done", nil
}

func waitForCounter(t *testing.T, counter *atomic.Int64, want int64, timeout time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for counter.Load() < want {
		require.True(t, time.Now().Before(deadline), "%s did not reach %d (got %d)", what, want, counter.Load())
		time.Sleep(100 * time.Millisecond)
	}
}

// TestCrashResume_JournaledSideEffectsSurviveRestart restarts Restate while an
// invocation is suspended on a durable timer and asserts:
//  1. the invocation resumes and completes after the restart, and
//  2. the side effect journaled before the crash is NOT re-executed on replay.
func TestCrashResume_JournaledSideEffectsSurviveRestart(t *testing.T) {
	probe := &crashProbe{step1: &atomic.Int64{}, step2: &atomic.Int64{}}
	env := restatetest.Start(t, restate.Reflect(probe))

	// The blocking ingress request rides a connection that dies with the
	// restart; the invocation itself is durable. Fire it from a goroutine and
	// observe progress purely through the probe's counters.
	go func() {
		_, _ = ingress.Object[restate.Void, string](
			env.Ingress(), "CrashProbe", "probe-1", "Run",
		).Request(context.Background(), restate.Void{})
	}()

	waitForCounter(t, probe.step1, 1, 10*time.Second, "step1 (pre-crash side effect)")
	require.Equal(t, int64(0), probe.step2.Load(), "step2 must not run before the timer fires")

	env.RestartRestate(t)

	waitForCounter(t, probe.step2, 1, 90*time.Second, "step2 (post-restart side effect)")
	assert.Equal(t, int64(1), probe.step1.Load(),
		"journal replay must not re-execute the pre-crash side effect")
	assert.Equal(t, int64(1), probe.step2.Load())
}

// TestCrashResume_DeploymentCompletesAfterRestart submits a multi-resource
// deployment, restarts Restate while the workflow is in flight, and asserts
// the deployment still reaches Complete with every resource provisioned.
func TestCrashResume_DeploymentCompletesAfterRestart(t *testing.T) {
	env := setupCoreStack(t)

	prefix := uniqueName(t, "crash")
	buckets := make([]string, 6)
	var tb strings.Builder
	tb.WriteString("resources: {\n")
	for i := range buckets {
		buckets[i] = fmt.Sprintf("%s-%d", prefix, i)
		fmt.Fprintf(&tb, `
	bucket%d: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: { name: %q }
		spec: { region: "us-east-1" }
	}
`, i, buckets[i])
	}
	tb.WriteString("}\n")
	template := tb.String()
	deployKey := "test-crash-resume-" + prefix

	_, err := ingress.Service[command.ApplyRequest, command.ApplyResponse](
		env.ingress, "PraxisCommandService", "Apply",
	).Request(t.Context(), command.ApplyRequest{
		Template:      template,
		DeploymentKey: deployKey,
		Variables:     accountVariables(),
	})
	require.NoError(t, err)

	// Restart as soon as the workflow reports progress. If the deployment
	// already finished, the test still verifies state survives a restart.
	interrupted := pollUntilBusyOrComplete(t, env, deployKey, 30*time.Second)
	env.env.RestartRestate(t)
	// Restart reassigns the mapped ingress port; refresh the test's client.
	env.ingress = env.env.Ingress()
	if !interrupted {
		t.Log("deployment completed before the restart window; asserting post-restart state only")
	}

	state := pollDeploymentState(t, env.ingress, deployKey,
		[]types.DeploymentStatus{types.DeploymentComplete}, 120*time.Second)
	require.Equal(t, types.DeploymentComplete, state.Status,
		"deployment must finish after the Restate restart — error: %v", state.Error)

	for _, bucket := range buckets {
		_, err := env.s3Client.HeadBucket(context.Background(), &s3sdk.HeadBucketInput{Bucket: aws.String(bucket)})
		require.NoError(t, err, "bucket %s must exist after crash-resume", bucket)
	}
}

// pollUntilBusyOrComplete waits until the deployment is observably in flight
// (Pending/Running) or already Complete. Returns true when the restart will
// interrupt live work.
func pollUntilBusyOrComplete(t *testing.T, env *coreTestEnv, deployKey string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, err := ingress.Object[restate.Void, *orchestratorDeploymentState](
			env.ingress, "DeploymentStateObj", deployKey, "GetState",
		).Request(t.Context(), restate.Void{})
		if err == nil && state != nil {
			switch state.Status {
			case types.DeploymentRunning, types.DeploymentPending:
				return true
			case types.DeploymentComplete:
				return false
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("deployment %s never became observable", deployKey)
	return false
}

// orchestratorDeploymentState mirrors the subset of the deployment state the
// poll needs; using a local type avoids importing internal/core/orchestrator
// just for one field.
type orchestratorDeploymentState struct {
	Status types.DeploymentStatus `json:"status"`
}
