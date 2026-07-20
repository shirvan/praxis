// MetricAlarm provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + alarm name.
// Metric alarms are region-scoped; the key combines the AWS region and alarm name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/metricalarm"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// MetricAlarmAdapter is the descriptor-driven adapter for MetricAlarm.
type MetricAlarmAdapter = GenericAdapter[metricalarm.MetricAlarmSpec, metricalarm.MetricAlarmOutputs, metricalarm.ObservedState]

func metricAlarmDescriptor() GenericDescriptor[metricalarm.MetricAlarmSpec, metricalarm.MetricAlarmOutputs, metricalarm.ObservedState] {
	return GenericDescriptor[metricalarm.MetricAlarmSpec, metricalarm.MetricAlarmOutputs, metricalarm.ObservedState]{
		Kind:  metricalarm.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (metricalarm.MetricAlarmSpec, error) {
			var parsed metricalarm.MetricAlarmSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return metricalarm.MetricAlarmSpec{}, fmt.Errorf("decode MetricAlarm spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return metricalarm.MetricAlarmSpec{}, fmt.Errorf("MetricAlarm metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return metricalarm.MetricAlarmSpec{}, fmt.Errorf("MetricAlarm spec.region is required")
			}
			if parsed.Dimensions == nil {
				parsed.Dimensions = map[string]string{}
			}
			if parsed.Tags == nil {
				parsed.Tags = map[string]string{}
			}
			parsed.AlarmName = name
			return parsed, nil
		},

		KeyFromSpec: func(spec metricalarm.MetricAlarmSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("alarm name", name); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, name), nil
		},

		ImportKey: func(region, resourceID string) (string, error) {
			if err := ValidateKeyPart("region", region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("resource ID", resourceID); err != nil {
				return "", err
			}
			return JoinKey(region, resourceID), nil
		},

		PrepareSpec: func(spec metricalarm.MetricAlarmSpec, key, account string) metricalarm.MetricAlarmSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out metricalarm.MetricAlarmOutputs) map[string]any {
			result := map[string]any{
				"alarmArn":   out.AlarmArn,
				"alarmName":  out.AlarmName,
				"stateValue": out.StateValue,
			}
			if out.StateReason != "" {
				result["stateReason"] = out.StateReason
			}
			return result
		},

		PlanIdentity: storedPlanIdentity[metricalarm.MetricAlarmSpec](func(out metricalarm.MetricAlarmOutputs) string { return out.AlarmName }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[metricalarm.MetricAlarmSpec, metricalarm.MetricAlarmOutputs, metricalarm.ObservedState] {
			return metricAlarmProbe(metricalarm.NewMetricAlarmAPI(awsclient.NewCloudWatchClient(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[metricalarm.MetricAlarmOutputs] {
			return metricAlarmLookupProbe(metricalarm.NewMetricAlarmAPI(awsclient.NewCloudWatchClient(cfg)))
		},

		DiffFields: func(desired metricalarm.MetricAlarmSpec, observed metricalarm.ObservedState, _ metricalarm.MetricAlarmOutputs) []types.FieldDiff {
			return metricalarm.ComputeFieldDiffs(desired, observed)
		},
	}
}

func metricAlarmLookupProbe(api metricalarm.MetricAlarmAPI) LookupProbeFunc[metricalarm.MetricAlarmOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (metricalarm.MetricAlarmOutputs, bool, error) {
		if strings.TrimSpace(filter.ID) != "" {
			return metricalarm.MetricAlarmOutputs{}, false, restate.TerminalError(
				fmt.Errorf("MetricAlarm lookup by id is not available; use name"),
				400,
			)
		}
		name := strings.TrimSpace(filter.Name)
		if name == "" {
			return metricalarm.MetricAlarmOutputs{}, false, restate.TerminalError(
				fmt.Errorf("MetricAlarm lookup requires name; tag-only lookup is not available"),
				400,
			)
		}
		observed, found, err := api.DescribeAlarm(ctx, name)
		if err != nil {
			if isLookupNotFound(err, metricalarm.IsNotFound) {
				return metricalarm.MetricAlarmOutputs{}, false, nil
			}
			return metricalarm.MetricAlarmOutputs{}, false, err
		}
		if !found || !matchesNativeLookupFilter(observed.AlarmName, observed.Tags, filter) {
			return metricalarm.MetricAlarmOutputs{}, false, nil
		}
		return metricalarm.MetricAlarmOutputs{
			AlarmArn:    observed.AlarmArn,
			AlarmName:   observed.AlarmName,
			StateValue:  observed.StateValue,
			StateReason: observed.StateReason,
		}, true, nil
	}
}

// metricAlarmProbe adapts the driver API to the generic plan probe shape. The
// driver's describe reports existence directly alongside the observed state.
func metricAlarmProbe(api metricalarm.MetricAlarmAPI) PlanProbeFunc[metricalarm.MetricAlarmSpec, metricalarm.MetricAlarmOutputs, metricalarm.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[metricalarm.MetricAlarmSpec, metricalarm.MetricAlarmOutputs]) (metricalarm.ObservedState, bool, error) {
		alarmName := input.Identity
		obs, found, err := api.DescribeAlarm(runCtx, alarmName)
		if err != nil {
			if metricalarm.IsNotFound(err) {
				return metricalarm.ObservedState{}, false, nil
			}
			return metricalarm.ObservedState{}, false, err
		}
		return obs, found, nil
	}
}

// NewMetricAlarmAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewMetricAlarmAdapterWithAuth(auth authservice.AuthClient) *MetricAlarmAdapter {
	return NewGenericAdapter(metricAlarmDescriptor(), auth)
}

// NewMetricAlarmAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewMetricAlarmAdapterWithAPI(api metricalarm.MetricAlarmAPI) *MetricAlarmAdapter {
	return NewGenericAdapterWithProbes(metricAlarmDescriptor(), metricAlarmProbe(api), metricAlarmLookupProbe(api))
}
