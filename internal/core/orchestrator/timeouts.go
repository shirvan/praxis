package orchestrator

import (
	"fmt"
	"time"

	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/pkg/types"
)

const (
	defaultCreateTimeout = 5 * time.Minute
	defaultUpdateTimeout = 5 * time.Minute
	defaultDeleteTimeout = 5 * time.Minute
)

func resolveProvisionTimeout(adapter provider.Adapter, lifecycle *types.LifecyclePolicy, isUpdate bool) (time.Duration, error) {
	defaults := defaultTimeoutsForAdapter(adapter)
	if lifecycle != nil && lifecycle.Timeouts != nil {
		if isUpdate && lifecycle.Timeouts.Update != "" {
			return parseDurationField("lifecycle.timeouts.update", lifecycle.Timeouts.Update)
		}
		if !isUpdate && lifecycle.Timeouts.Create != "" {
			return parseDurationField("lifecycle.timeouts.create", lifecycle.Timeouts.Create)
		}
	}
	if isUpdate && defaults.Update != "" {
		return parseDurationField("adapter default update timeout", defaults.Update)
	}
	if !isUpdate && defaults.Create != "" {
		return parseDurationField("adapter default create timeout", defaults.Create)
	}
	if isUpdate {
		return defaultUpdateTimeout, nil
	}
	return defaultCreateTimeout, nil
}

func resolveDeleteTimeout(adapter provider.Adapter, lifecycle *types.LifecyclePolicy) (time.Duration, error) {
	if lifecycle != nil && lifecycle.Timeouts != nil && lifecycle.Timeouts.Delete != "" {
		return parseDurationField("lifecycle.timeouts.delete", lifecycle.Timeouts.Delete)
	}
	defaults := defaultTimeoutsForAdapter(adapter)
	if defaults.Delete != "" {
		return parseDurationField("adapter default delete timeout", defaults.Delete)
	}
	return defaultDeleteTimeout, nil
}

func defaultTimeoutsForAdapter(adapter provider.Adapter) types.ResourceTimeouts {
	timeoutProvider, ok := adapter.(provider.TimeoutDefaultsProvider)
	if !ok {
		return types.ResourceTimeouts{}
	}
	return timeoutProvider.DefaultTimeouts()
}

func parseDurationField(name, value string) (time.Duration, error) {
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}