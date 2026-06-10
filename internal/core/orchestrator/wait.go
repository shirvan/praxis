package orchestrator

import (
	"fmt"
	"maps"
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
			maps.Copy(merged, outputs)
			maps.Copy(merged, result.Outputs)
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
		if err := restate.Sleep(ctx, pollInterval); err != nil {
			return nil, err
		}
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
	interval, err := time.ParseDuration(lifecycle.Wait.PollInterval)
	if err != nil {
		return 0, err
	}
	if interval <= 0 {
		// A zero/negative interval would hot-loop WaitReady calls and grow the
		// journal until maxWait expires.
		return 0, fmt.Errorf("lifecycle.wait.pollInterval must be positive, got %q", lifecycle.Wait.PollInterval)
	}
	return interval, nil
}

func waitMaxWait(lifecycle *types.LifecyclePolicy) (time.Duration, error) {
	if lifecycle == nil || lifecycle.Wait == nil || lifecycle.Wait.MaxWait == "" {
		return 0, nil
	}
	return time.ParseDuration(lifecycle.Wait.MaxWait)
}