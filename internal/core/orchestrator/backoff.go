package orchestrator

import (
	"fmt"
	"hash/fnv"
	"time"

	"github.com/shirvan/praxis/pkg/types"
)

const (
	defaultRetryMaxRetries = 3
	defaultRetryBaseDelay  = 5 * time.Second
	defaultRetryMaxDelay   = 2 * time.Minute
)

func resolveRetryConfig(plan DeploymentPlan, lifecycle *types.LifecyclePolicy) (RetryConfig, error) {
	resolved := RetryConfig{
		MaxRetries: defaultRetryMaxRetries,
		BaseDelay:  defaultRetryBaseDelay,
		MaxDelay:   defaultRetryMaxDelay,
	}
	if plan.RetryConfig != nil {
		resolved = *plan.RetryConfig
		if resolved.MaxRetries < 0 {
			resolved.MaxRetries = 0
		}
		if resolved.BaseDelay <= 0 {
			resolved.BaseDelay = defaultRetryBaseDelay
		}
		if resolved.MaxDelay <= 0 {
			resolved.MaxDelay = defaultRetryMaxDelay
		}
	}
	if lifecycle == nil || lifecycle.Retry == nil {
		return resolved, nil
	}
	if lifecycle.Retry.MaxRetries != nil {
		resolved.MaxRetries = max(0, *lifecycle.Retry.MaxRetries)
	}
	if lifecycle.Retry.BaseDelay != "" {
		parsed, err := time.ParseDuration(lifecycle.Retry.BaseDelay)
		if err != nil {
			return RetryConfig{}, fmt.Errorf("parse lifecycle.retry.baseDelay: %w", err)
		}
		resolved.BaseDelay = parsed
	}
	if lifecycle.Retry.MaxDelay != "" {
		parsed, err := time.ParseDuration(lifecycle.Retry.MaxDelay)
		if err != nil {
			return RetryConfig{}, fmt.Errorf("parse lifecycle.retry.maxDelay: %w", err)
		}
		resolved.MaxDelay = parsed
	}
	if resolved.BaseDelay <= 0 {
		resolved.BaseDelay = defaultRetryBaseDelay
	}
	if resolved.MaxDelay <= 0 {
		resolved.MaxDelay = defaultRetryMaxDelay
	}
	if resolved.MaxDelay < resolved.BaseDelay {
		resolved.MaxDelay = resolved.BaseDelay
	}
	return resolved, nil
}

func nextRetryDelay(resourceName string, attempt int, config RetryConfig, retryAfter time.Duration) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	capDelay := config.BaseDelay
	for i := 1; i < attempt; i++ {
		if capDelay >= config.MaxDelay/2 {
			capDelay = config.MaxDelay
			break
		}
		capDelay *= 2
	}
	if capDelay > config.MaxDelay {
		capDelay = config.MaxDelay
	}
	actual := deterministicJitter(resourceName, attempt, capDelay)
	if retryAfter > actual {
		actual = retryAfter
	}
	if actual <= 0 {
		return config.BaseDelay
	}
	return actual
}

func deterministicJitter(resourceName string, attempt int, maxDelay time.Duration) time.Duration {
	if maxDelay <= 0 {
		return 0
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(fmt.Sprintf("%s:%d", resourceName, attempt)))
	return time.Duration(hash.Sum64()%uint64(maxDelay+1))
}