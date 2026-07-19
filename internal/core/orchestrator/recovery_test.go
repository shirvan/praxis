package orchestrator

import (
	"testing"
	"time"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type recoveryWorkflowStub struct{}

func (recoveryWorkflowStub) ServiceName() string { return DeploymentWorkflowServiceName }

func (recoveryWorkflowStub) Run(restate.WorkflowContext, DeploymentPlan) (DeploymentResult, error) {
	return DeploymentResult{}, nil
}

func TestResolveRecoveryPolicy(t *testing.T) {
	t.Run("managed defaults automatic without deadline", func(t *testing.T) {
		mode, timeout, err := resolveRecoveryPolicy(nil, types.ModeManaged)
		require.NoError(t, err)
		assert.Equal(t, types.RecoveryModeAutomatic, mode)
		assert.Zero(t, timeout)
	})

	t.Run("configured timeout", func(t *testing.T) {
		mode, timeout, err := resolveRecoveryPolicy(&types.LifecyclePolicy{Recovery: &types.RecoveryPolicy{
			Mode: types.RecoveryModeAutomatic, Timeout: "15m",
		}}, types.ModeManaged)
		require.NoError(t, err)
		assert.Equal(t, types.RecoveryModeAutomatic, mode)
		assert.Equal(t, 15*time.Minute, timeout)
	})

	t.Run("observed is always manual", func(t *testing.T) {
		mode, _, err := resolveRecoveryPolicy(&types.LifecyclePolicy{Recovery: &types.RecoveryPolicy{
			Mode: types.RecoveryModeAutomatic,
		}}, types.ModeObserved)
		require.NoError(t, err)
		assert.Equal(t, types.RecoveryModeManual, mode)
	})

	t.Run("invalid mode rejected", func(t *testing.T) {
		_, _, err := resolveRecoveryPolicy(&types.LifecyclePolicy{Recovery: &types.RecoveryPolicy{
			Mode: "Sometimes",
		}}, types.ModeManaged)
		require.ErrorContains(t, err, "invalid lifecycle.recovery.mode")
	})

	t.Run("invalid timeout rejected", func(t *testing.T) {
		_, _, err := resolveRecoveryPolicy(&types.LifecyclePolicy{Recovery: &types.RecoveryPolicy{
			Timeout: "later",
		}}, types.ModeManaged)
		require.ErrorContains(t, err, "invalid lifecycle.recovery.timeout")
	})
}

func TestHandleExternalDeleteStateTransitions(t *testing.T) {
	env := restatetest.Start(t,
		restate.Reflect(DeploymentStateObj{}),
		restate.Reflect(ResourceEventOwnerObj{}),
		restate.Reflect(recoveryWorkflowStub{}),
	)
	client := env.Ingress()

	initDeployment := func(t *testing.T, key string, lifecycle *types.LifecyclePolicy) {
		t.Helper()
		_, err := ingress.Object[DeploymentPlan, int64](
			client, DeploymentStateServiceName, key, "InitDeployment",
		).Request(t.Context(), DeploymentPlan{
			Key: key,
			Resources: []PlanResource{{
				Name: "bucket", Kind: "S3Bucket", DriverService: "S3Bucket",
				Key: "us-east-1~" + key, Lifecycle: lifecycle,
			}},
			CreatedAt: time.Now().UTC(),
		})
		require.NoError(t, err)
	}

	getState := func(t *testing.T, key string) *DeploymentState {
		t.Helper()
		state, err := ingress.Object[restate.Void, *DeploymentState](
			client, DeploymentStateServiceName, key, "GetState",
		).Request(t.Context(), restate.Void{})
		require.NoError(t, err)
		require.NotNil(t, state)
		return state
	}

	t.Run("manual recovery records visibility without scheduling", func(t *testing.T) {
		const key = "manual-recovery"
		initDeployment(t, key, &types.LifecyclePolicy{Recovery: &types.RecoveryPolicy{
			Mode: types.RecoveryModeManual,
		}})
		_, err := ingress.Object[StatusUpdate, restate.Void](
			client, DeploymentStateServiceName, key, "SetStatus",
		).Request(t.Context(), StatusUpdate{Status: types.DeploymentComplete, UpdatedAt: time.Now().UTC()})
		require.NoError(t, err)

		result, err := ingress.Object[ExternalDeleteRequest, RecoveryResult](
			client, DeploymentStateServiceName, key, "HandleExternalDelete",
		).Request(t.Context(), ExternalDeleteRequest{
			ResourceName: "bucket", Mode: types.ModeManaged, Error: "bucket disappeared",
		})
		require.NoError(t, err)
		assert.True(t, result.Manual)
		assert.False(t, result.Started)

		state := getState(t, key)
		assert.Equal(t, types.DeploymentComplete, state.Status)
		assert.Equal(t, types.DeploymentResourceError, state.Resources["bucket"].Status)
		assert.Equal(t, "bucket disappeared", state.Resources["bucket"].Error)
		require.Len(t, state.Resources["bucket"].Conditions, 1)
		assert.Equal(t, types.ReasonReplacementRequired, state.Resources["bucket"].Conditions[0].Reason)
	})

	t.Run("active workflow remains authoritative", func(t *testing.T) {
		const key = "active-recovery"
		initDeployment(t, key, nil)

		result, err := ingress.Object[ExternalDeleteRequest, RecoveryResult](
			client, DeploymentStateServiceName, key, "HandleExternalDelete",
		).Request(t.Context(), ExternalDeleteRequest{
			ResourceName: "bucket", Mode: types.ModeManaged, Error: "stale drift event",
		})
		require.NoError(t, err)
		assert.False(t, result.Started)
		assert.Contains(t, result.Reason, "already Pending")

		state := getState(t, key)
		assert.Equal(t, types.DeploymentPending, state.Status)
		assert.Equal(t, types.DeploymentResourcePending, state.Resources["bucket"].Status)
		assert.Empty(t, state.Resources["bucket"].Error)
		assert.Empty(t, state.Resources["bucket"].Conditions)
	})

	t.Run("automatic recovery schedules one replacement attempt", func(t *testing.T) {
		const key = "automatic-recovery"
		initDeployment(t, key, nil)
		_, err := ingress.Object[StatusUpdate, restate.Void](
			client, DeploymentStateServiceName, key, "SetStatus",
		).Request(t.Context(), StatusUpdate{Status: types.DeploymentComplete, UpdatedAt: time.Now().UTC()})
		require.NoError(t, err)

		result, err := ingress.Object[ExternalDeleteRequest, RecoveryResult](
			client, DeploymentStateServiceName, key, "HandleExternalDelete",
		).Request(t.Context(), ExternalDeleteRequest{
			ResourceName: "bucket", Mode: types.ModeManaged, Error: "bucket disappeared",
		})
		require.NoError(t, err)
		assert.True(t, result.Started)
		assert.False(t, result.Manual)

		state := getState(t, key)
		assert.Equal(t, types.DeploymentPending, state.Status)
		assert.Equal(t, types.DeploymentResourcePending, state.Resources["bucket"].Status)
		assert.Equal(t, 1, state.Resources["bucket"].RecoveryAttempts)
		require.NotNil(t, state.Resources["bucket"].RecoveryStartedAt)
		require.Len(t, state.Resources["bucket"].Conditions, 1)
		assert.Equal(t, types.ReasonRecoveryScheduled, state.Resources["bucket"].Conditions[0].Reason)
	})
}
