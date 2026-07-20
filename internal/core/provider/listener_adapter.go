// Listener provider adapter — descriptor for the GenericAdapter.
//
// Key scope: custom.
// Key parts: load balancer ARN + port.
// Listeners are scoped to a load balancer; the key combines the LB ARN and port.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/listener"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// ListenerAdapter is the descriptor-driven adapter for Listener.
type ListenerAdapter = GenericAdapter[listener.ListenerSpec, listener.ListenerOutputs, listener.ObservedState]

func listenerDescriptor() GenericDescriptor[listener.ListenerSpec, listener.ListenerOutputs, listener.ObservedState] {
	return GenericDescriptor[listener.ListenerSpec, listener.ListenerOutputs, listener.ObservedState]{
		Kind:  listener.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (listener.ListenerSpec, error) {
			var spec listener.ListenerSpec
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return listener.ListenerSpec{}, fmt.Errorf("decode Listener spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return listener.ListenerSpec{}, fmt.Errorf("listener metadata.name is required")
			}
			if spec.LoadBalancerArn == "" {
				return listener.ListenerSpec{}, fmt.Errorf("listener spec.loadBalancerArn is required")
			}
			spec.Account = ""
			if spec.Tags == nil {
				spec.Tags = map[string]string{}
			}
			if spec.Tags["praxis:listener-name"] == "" {
				spec.Tags["praxis:listener-name"] = name
			}
			return spec, nil
		},

		KeyFromSpec: func(spec listener.ListenerSpec, metadataName string) (string, error) {
			region := spec.Region
			if region == "" {
				region = extractRegionFromLBArn(spec.LoadBalancerArn)
			}
			if region == "" {
				return "", fmt.Errorf("cannot determine region: set spec.region or provide a valid loadBalancerArn")
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("listener name", name); err != nil {
				return "", err
			}
			return JoinKey(region, name), nil
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

		PrepareSpec: func(spec listener.ListenerSpec, _ string, account string) listener.ListenerSpec {
			spec.Account = account
			return spec
		},

		NormalizeOutputs: func(out listener.ListenerOutputs) map[string]any {
			return map[string]any{
				"listenerArn": out.ListenerArn,
				"port":        out.Port,
				"protocol":    out.Protocol,
			}
		},

		PlanIdentity: storedPlanIdentity[listener.ListenerSpec](func(out listener.ListenerOutputs) string { return out.ListenerArn }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[listener.ListenerSpec, listener.ListenerOutputs, listener.ObservedState] {
			return listenerProbe(listener.NewListenerAPI(awsclient.NewELBv2Client(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[listener.ListenerOutputs] {
			return listenerLookupProbe(listener.NewListenerAPI(awsclient.NewELBv2Client(cfg)))
		},

		DiffFields: func(desired listener.ListenerSpec, observed listener.ObservedState, _ listener.ListenerOutputs) []types.FieldDiff {
			return listener.ComputeFieldDiffs(desired, observed)
		},
	}
}

func listenerLookupProbe(api listener.ListenerAPI) LookupProbeFunc[listener.ListenerOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (listener.ListenerOutputs, bool, error) {
		listenerARN := strings.TrimSpace(filter.ID)
		if listenerARN == "" || strings.TrimSpace(filter.Name) != "" || len(filter.Tag) > 0 {
			return listener.ListenerOutputs{}, false, restate.TerminalError(
				fmt.Errorf("listener lookup supports listener ARN via id only"),
				400,
			)
		}
		observed, err := api.DescribeListener(ctx, listenerARN)
		if err != nil {
			if isLookupNotFound(err, listener.IsNotFound) {
				return listener.ListenerOutputs{}, false, nil
			}
			return listener.ListenerOutputs{}, false, err
		}
		if observed.ListenerArn != listenerARN {
			return listener.ListenerOutputs{}, false, nil
		}
		return listener.ListenerOutputs{
			ListenerArn: observed.ListenerArn,
			Port:        observed.Port,
			Protocol:    observed.Protocol,
		}, true, nil
	}
}

// listenerProbe adapts the driver API to the generic plan probe shape.
func listenerProbe(api listener.ListenerAPI) PlanProbeFunc[listener.ListenerSpec, listener.ListenerOutputs, listener.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[listener.ListenerSpec, listener.ListenerOutputs]) (listener.ObservedState, bool, error) {
		listenerArn := input.Identity
		obs, err := api.DescribeListener(runCtx, listenerArn)
		if err != nil {
			if listener.IsNotFound(err) {
				return listener.ObservedState{}, false, nil
			}
			return listener.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewListenerAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewListenerAdapterWithAuth(auth authservice.AuthClient) *ListenerAdapter {
	return NewGenericAdapter(listenerDescriptor(), auth)
}

// NewListenerAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewListenerAdapterWithAPI(api listener.ListenerAPI) *ListenerAdapter {
	return NewGenericAdapterWithProbes(listenerDescriptor(), listenerProbe(api), listenerLookupProbe(api))
}

func extractRegionFromLBArn(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 4 {
		return parts[3]
	}
	return ""
}
