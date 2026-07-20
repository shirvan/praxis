// Route53HealthCheck provider adapter — descriptor for the GenericAdapter.
//
// Key scope: global.
// Key parts: the resource's metadata.name (mapped to the Name tag).
// Route 53 health checks are global; the key is the health check name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/route53healthcheck"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// Route53HealthCheckAdapter is the descriptor-driven adapter for Route53HealthCheck.
type Route53HealthCheckAdapter = GenericAdapter[route53healthcheck.HealthCheckSpec, route53healthcheck.HealthCheckOutputs, route53healthcheck.ObservedState]

func route53HealthCheckDescriptor() GenericDescriptor[route53healthcheck.HealthCheckSpec, route53healthcheck.HealthCheckOutputs, route53healthcheck.ObservedState] {
	return GenericDescriptor[route53healthcheck.HealthCheckSpec, route53healthcheck.HealthCheckOutputs, route53healthcheck.ObservedState]{
		Kind:  route53healthcheck.ServiceName,
		Scope: KeyScopeGlobal,

		DecodeSpec: func(rawSpec json.RawMessage, _ string) (route53healthcheck.HealthCheckSpec, error) {
			var spec route53healthcheck.HealthCheckSpec
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return route53healthcheck.HealthCheckSpec{}, fmt.Errorf("decode Route53HealthCheck spec: %w", err)
			}
			if spec.Tags == nil {
				spec.Tags = map[string]string{}
			}
			// Only the orchestrator (not the template author) may set the account.
			spec.Account = ""
			return spec, nil
		},

		KeyFromSpec: func(_ route53healthcheck.HealthCheckSpec, metadataName string) (string, error) {
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("health check name", name); err != nil {
				return "", err
			}
			return name, nil
		},

		ImportKey: func(_, resourceID string) (string, error) {
			if err := ValidateKeyPart("resource ID", resourceID); err != nil {
				return "", err
			}
			return strings.TrimSpace(resourceID), nil
		},

		PrepareSpec: func(spec route53healthcheck.HealthCheckSpec, key, account string) route53healthcheck.HealthCheckSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out route53healthcheck.HealthCheckOutputs) map[string]any {
			return map[string]any{"healthCheckId": out.HealthCheckId}
		},

		PlanIdentity: storedPlanIdentity[route53healthcheck.HealthCheckSpec](func(out route53healthcheck.HealthCheckOutputs) string { return out.HealthCheckId }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[route53healthcheck.HealthCheckSpec, route53healthcheck.HealthCheckOutputs, route53healthcheck.ObservedState] {
			return route53HealthCheckProbe(route53healthcheck.NewHealthCheckAPI(awsclient.NewRoute53Client(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[route53healthcheck.HealthCheckOutputs] {
			return route53HealthCheckLookupProbe(route53healthcheck.NewHealthCheckAPI(awsclient.NewRoute53Client(cfg)))
		},

		DiffFields: func(desired route53healthcheck.HealthCheckSpec, observed route53healthcheck.ObservedState, _ route53healthcheck.HealthCheckOutputs) []types.FieldDiff {
			return route53healthcheck.ComputeFieldDiffs(desired, observed)
		},
	}
}

func route53HealthCheckLookupProbe(api route53healthcheck.HealthCheckAPI) LookupProbeFunc[route53healthcheck.HealthCheckOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (route53healthcheck.HealthCheckOutputs, bool, error) {
		healthCheckID := strings.TrimSpace(filter.ID)
		if healthCheckID == "" {
			return route53healthcheck.HealthCheckOutputs{}, false, restate.TerminalError(
				fmt.Errorf("Route53HealthCheck lookup supports id; name-only and tag-only lookup are not available"),
				400,
			)
		}
		observed, err := api.DescribeHealthCheck(ctx, healthCheckID)
		if err != nil {
			if isLookupNotFound(err, route53healthcheck.IsNotFound) {
				return route53healthcheck.HealthCheckOutputs{}, false, nil
			}
			return route53healthcheck.HealthCheckOutputs{}, false, err
		}
		if observed.HealthCheckId != healthCheckID || !matchesLookupTags(observed.Tags, LookupFilter{Name: filter.Name, Tag: filter.Tag}) {
			return route53healthcheck.HealthCheckOutputs{}, false, nil
		}
		return route53healthcheck.HealthCheckOutputs{HealthCheckId: observed.HealthCheckId}, true, nil
	}
}

// route53HealthCheckProbe adapts the driver API to the generic plan probe shape.
func route53HealthCheckProbe(api route53healthcheck.HealthCheckAPI) PlanProbeFunc[route53healthcheck.HealthCheckSpec, route53healthcheck.HealthCheckOutputs, route53healthcheck.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[route53healthcheck.HealthCheckSpec, route53healthcheck.HealthCheckOutputs]) (route53healthcheck.ObservedState, bool, error) {
		healthCheckID := input.Identity
		obs, err := api.DescribeHealthCheck(runCtx, healthCheckID)
		if err != nil {
			if route53healthcheck.IsNotFound(err) {
				return route53healthcheck.ObservedState{}, false, nil
			}
			return route53healthcheck.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewRoute53HealthCheckAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewRoute53HealthCheckAdapterWithAuth(auth authservice.AuthClient) *Route53HealthCheckAdapter {
	return NewGenericAdapter(route53HealthCheckDescriptor(), auth)
}

// NewRoute53HealthCheckAdapterWithAPI builds an adapter with a fixed planning
// API. Used by tests.
func NewRoute53HealthCheckAdapterWithAPI(api route53healthcheck.HealthCheckAPI) *Route53HealthCheckAdapter {
	return NewGenericAdapterWithProbes(route53HealthCheckDescriptor(), route53HealthCheckProbe(api), route53HealthCheckLookupProbe(api))
}
