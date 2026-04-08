package orchestrator

import (
	"fmt"
	"time"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/pkg/types"
)

const defaultWaitPollInterval = 10 * time.Second

func waitForResourceReady(
	ctx restate.WorkflowContext,
	waiter provider.ReadyWaiter,
	key string,
	outputs map[string]any,
	maxWait time.Duration,
	lifecycle *types.LifecyclePolicy,
) (map[string]any, error) {
	if waiter == nil || !waitEnabled(lifecycle) {
		return outputs, nil
	}
	if maxWait <= 0 {
		return nil, fmt.Errorf("wait ready exceeded the resource timeout budget")
	}
	pollInterval, err := waitPollInterval(lifecycle)
	if err != nil {
		return nil, err
	}
	if override, err := waitMaxWait(lifecycle); err != nil {
		return nil, err
	} else if override > 0 && override < maxWait {
		maxWait = override
	}
	startedAt, err := currentTime(ctx)
	if err != nil {
		return nil, err
	}
	lastMessage := ""
	for {
		result, err := waiter.WaitReady(ctx, key)
		if err != nil {
			return nil, err
		}
		lastMessage = result.Message
		if result.Ready {
			if len(result.Outputs) == 0 {
				return outputs, nil
			}
			merged := make(map[string]any, len(outputs)+len(result.Outputs))
			for key, value := range outputs {
				merged[key] = value
			}
			for key, value := range result.Outputs {
				merged[key] = value
			}
			return merged, nil
		}
		now, err := currentTime(ctx)
		if err != nil {
			return nil, err
		}
		if now.Sub(startedAt) >= maxWait {
			if lastMessage == "" {
				lastMessage = "resource did not report ready"
			}
			return nil, fmt.Errorf("wait ready timed out after %s: %s", maxWait, lastMessage)
		}
		_ = restate.Sleep(ctx, pollInterval)
	}
}

func waitEnabled(lifecycle *types.LifecyclePolicy) bool {
	if lifecycle == nil || lifecycle.Wait == nil || lifecycle.Wait.Enabled == nil {
		return true
	}
	return *lifecycle.Wait.Enabled
}

func waitPollInterval(lifecycle *types.LifecyclePolicy) (time.Duration, error) {
	if lifecycle == nil || lifecycle.Wait == nil || lifecycle.Wait.PollInterval == "" {
		return defaultWaitPollInterval, nil
	}
	return time.ParseDuration(lifecycle.Wait.PollInterval)
}

func waitMaxWait(lifecycle *types.LifecyclePolicy) (time.Duration, error) {
	if lifecycle == nil || lifecycle.Wait == nil || lifecycle.Wait.MaxWait == "" {
		return 0, nil
	}
	return time.ParseDuration(lifecycle.Wait.MaxWait)
}