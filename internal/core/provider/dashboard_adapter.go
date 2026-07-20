// Dashboard provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + dashboard name.
// CloudWatch dashboards are keyed by the AWS region and dashboard name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/dashboard"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// DashboardAdapter is the descriptor-driven adapter for Dashboard.
type DashboardAdapter = GenericAdapter[dashboard.DashboardSpec, dashboard.DashboardOutputs, dashboard.ObservedState]

func dashboardDescriptor() GenericDescriptor[dashboard.DashboardSpec, dashboard.DashboardOutputs, dashboard.ObservedState] {
	return GenericDescriptor[dashboard.DashboardSpec, dashboard.DashboardOutputs, dashboard.ObservedState]{
		Kind:  dashboard.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (dashboard.DashboardSpec, error) {
			var parsed dashboard.DashboardSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return dashboard.DashboardSpec{}, fmt.Errorf("decode dashboard spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return dashboard.DashboardSpec{}, fmt.Errorf("dashboard metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return dashboard.DashboardSpec{}, fmt.Errorf("dashboard spec.region is required")
			}
			parsed.DashboardName = name
			return parsed, nil
		},

		KeyFromSpec: func(spec dashboard.DashboardSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("dashboard name", name); err != nil {
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

		PrepareSpec: func(spec dashboard.DashboardSpec, key, account string) dashboard.DashboardSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out dashboard.DashboardOutputs) map[string]any {
			return map[string]any{
				"dashboardArn":  out.DashboardArn,
				"dashboardName": out.DashboardName,
			}
		},

		PlanIdentity: storedPlanIdentity[dashboard.DashboardSpec](func(out dashboard.DashboardOutputs) string { return out.DashboardName }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[dashboard.DashboardSpec, dashboard.DashboardOutputs, dashboard.ObservedState] {
			return dashboardProbe(dashboard.NewDashboardAPI(awsclient.NewCloudWatchClient(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[dashboard.DashboardOutputs] {
			return dashboardLookupProbe(dashboard.NewDashboardAPI(awsclient.NewCloudWatchClient(cfg)))
		},

		DiffFields: func(desired dashboard.DashboardSpec, observed dashboard.ObservedState, _ dashboard.DashboardOutputs) []types.FieldDiff {
			return dashboard.ComputeFieldDiffs(desired, observed)
		},
	}
}

func dashboardLookupProbe(api dashboard.DashboardAPI) LookupProbeFunc[dashboard.DashboardOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (dashboard.DashboardOutputs, bool, error) {
		if strings.TrimSpace(filter.ID) != "" {
			return dashboard.DashboardOutputs{}, false, restate.TerminalError(
				fmt.Errorf("Dashboard lookup by id is not available; use name"),
				400,
			)
		}
		if len(filter.Tag) != 0 {
			return dashboard.DashboardOutputs{}, false, restate.TerminalError(
				fmt.Errorf("Dashboard lookup does not support tags"),
				400,
			)
		}
		name := strings.TrimSpace(filter.Name)
		if name == "" {
			return dashboard.DashboardOutputs{}, false, restate.TerminalError(
				fmt.Errorf("Dashboard lookup requires name"),
				400,
			)
		}
		observed, found, err := api.GetDashboard(ctx, name)
		if err != nil {
			if isLookupNotFound(err, dashboard.IsNotFound) {
				return dashboard.DashboardOutputs{}, false, nil
			}
			return dashboard.DashboardOutputs{}, false, err
		}
		if !found || observed.DashboardName != name {
			return dashboard.DashboardOutputs{}, false, nil
		}
		return dashboard.DashboardOutputs{
			DashboardArn:  observed.DashboardArn,
			DashboardName: observed.DashboardName,
		}, true, nil
	}
}

// dashboardProbe adapts the driver API to the generic plan probe shape. The
// driver's describe reports existence directly alongside the observed state.
func dashboardProbe(api dashboard.DashboardAPI) PlanProbeFunc[dashboard.DashboardSpec, dashboard.DashboardOutputs, dashboard.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[dashboard.DashboardSpec, dashboard.DashboardOutputs]) (dashboard.ObservedState, bool, error) {
		dashboardName := input.Identity
		obs, found, err := api.GetDashboard(runCtx, dashboardName)
		if err != nil {
			if dashboard.IsNotFound(err) {
				return dashboard.ObservedState{}, false, nil
			}
			return dashboard.ObservedState{}, false, err
		}
		return obs, found, nil
	}
}

// NewDashboardAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewDashboardAdapterWithAuth(auth authservice.AuthClient) *DashboardAdapter {
	return NewGenericAdapter(dashboardDescriptor(), auth)
}

// NewDashboardAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewDashboardAdapterWithAPI(api dashboard.DashboardAPI) *DashboardAdapter {
	return NewGenericAdapterWithProbes(dashboardDescriptor(), dashboardProbe(api), dashboardLookupProbe(api))
}
