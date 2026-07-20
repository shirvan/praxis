// Route53HostedZone provider adapter — descriptor for the GenericAdapter.
//
// Key scope: global (DNS zones are global).
// Key parts: zone name.
// Route 53 hosted zones are global; the key is the zone name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/route53zone"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type Route53HostedZoneAdapter struct {
	*GenericAdapter[route53zone.HostedZoneSpec, route53zone.HostedZoneOutputs, route53zone.ObservedState]
}

func route53HostedZoneDescriptor() GenericDescriptor[route53zone.HostedZoneSpec, route53zone.HostedZoneOutputs, route53zone.ObservedState] {
	return GenericDescriptor[route53zone.HostedZoneSpec, route53zone.HostedZoneOutputs, route53zone.ObservedState]{
		Kind:  route53zone.ServiceName,
		Scope: KeyScopeGlobal,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (route53zone.HostedZoneSpec, error) {
			var spec route53zone.HostedZoneSpec
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return route53zone.HostedZoneSpec{}, fmt.Errorf("decode Route53HostedZone spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return route53zone.HostedZoneSpec{}, fmt.Errorf("Route53HostedZone metadata.name is required")
			}
			spec.Name = strings.ToLower(strings.TrimSuffix(name, "."))
			if spec.Tags == nil {
				spec.Tags = map[string]string{}
			}
			// Only the orchestrator (not the template author) may set the account.
			spec.Account = ""
			return spec, nil
		},

		KeyFromSpec: func(spec route53zone.HostedZoneSpec, _ string) (string, error) {
			if err := ValidateKeyPart("hosted zone name", spec.Name); err != nil {
				return "", err
			}
			return spec.Name, nil
		},

		ImportKey: func(_, resourceID string) (string, error) {
			if err := ValidateKeyPart("resource ID", resourceID); err != nil {
				return "", err
			}
			return strings.TrimSpace(resourceID), nil
		},

		PrepareSpec: func(spec route53zone.HostedZoneSpec, key, account string) route53zone.HostedZoneSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out route53zone.HostedZoneOutputs) map[string]any {
			return map[string]any{"hostedZoneId": out.HostedZoneId, "name": out.Name, "nameServers": out.NameServers, "isPrivate": out.IsPrivate, "recordCount": out.RecordCount}
		},

		PlanIdentity: storedPlanIdentity[route53zone.HostedZoneSpec](func(out route53zone.HostedZoneOutputs) string { return out.HostedZoneId }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[route53zone.HostedZoneSpec, route53zone.HostedZoneOutputs, route53zone.ObservedState] {
			return route53HostedZoneProbe(route53zone.NewHostedZoneAPI(awsclient.NewRoute53Client(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[route53zone.HostedZoneOutputs] {
			return route53HostedZoneLookupProbe(route53zone.NewHostedZoneAPI(awsclient.NewRoute53Client(cfg)))
		},

		DiffFields: func(desired route53zone.HostedZoneSpec, observed route53zone.ObservedState, _ route53zone.HostedZoneOutputs) []types.FieldDiff {
			return route53zone.ComputeFieldDiffs(desired, observed)
		},
	}
}

// route53HostedZoneProbe adapts the driver API to the generic plan probe shape.
func route53HostedZoneProbe(api route53zone.HostedZoneAPI) PlanProbeFunc[route53zone.HostedZoneSpec, route53zone.HostedZoneOutputs, route53zone.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[route53zone.HostedZoneSpec, route53zone.HostedZoneOutputs]) (route53zone.ObservedState, bool, error) {
		hostedZoneID := input.Identity
		obs, err := api.DescribeHostedZone(runCtx, hostedZoneID)
		if err != nil {
			if route53zone.IsNotFound(err) {
				return route53zone.ObservedState{}, false, nil
			}
			return route53zone.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

func route53HostedZoneLookupProbe(api route53zone.HostedZoneAPI) LookupProbeFunc[route53zone.HostedZoneOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (route53zone.HostedZoneOutputs, bool, error) {
		observed, err := lookupHostedZone(ctx, api, filter)
		if err != nil {
			if isLookupNotFound(err, route53zone.IsNotFound) {
				return route53zone.HostedZoneOutputs{}, false, nil
			}
			return route53zone.HostedZoneOutputs{}, false, err
		}
		if !matchesHostedZoneFilter(observed, filter) {
			return route53zone.HostedZoneOutputs{}, false, nil
		}
		return route53zone.HostedZoneOutputs{
			HostedZoneId: observed.HostedZoneId,
			Name:         observed.Name,
			NameServers:  observed.NameServers,
			IsPrivate:    observed.IsPrivate,
			RecordCount:  observed.RecordCount,
		}, true, nil
	}
}

// NewRoute53HostedZoneAdapterWithAuth builds the production adapter; plan- and
// lookup-time credentials are resolved through the Auth Service.
func NewRoute53HostedZoneAdapterWithAuth(auth authservice.AuthClient) *Route53HostedZoneAdapter {
	return &Route53HostedZoneAdapter{
		GenericAdapter: NewGenericAdapter(route53HostedZoneDescriptor(), auth),
	}
}

// NewRoute53HostedZoneAdapterWithAPI builds an adapter with a fixed planning
// API used for both Plan probes and Lookup. Used by tests.
func NewRoute53HostedZoneAdapterWithAPI(api route53zone.HostedZoneAPI) *Route53HostedZoneAdapter {
	return &Route53HostedZoneAdapter{
		GenericAdapter: NewGenericAdapterWithProbes(route53HostedZoneDescriptor(), route53HostedZoneProbe(api), route53HostedZoneLookupProbe(api)),
	}
}

func lookupHostedZone(ctx restate.RunContext, api route53zone.HostedZoneAPI, filter LookupFilter) (route53zone.ObservedState, error) {
	if strings.TrimSpace(filter.ID) != "" {
		return api.DescribeHostedZone(ctx, strings.TrimSpace(filter.ID))
	}
	var id string
	var err error
	switch {
	case strings.TrimSpace(filter.Name) != "":
		id, err = api.FindHostedZoneByName(ctx, normalizeHostedZoneLookupName(filter.Name))
	case len(filter.Tag) > 0:
		id, err = api.FindHostedZoneByTags(ctx, filter.Tag)
	default:
		return route53zone.ObservedState{}, fmt.Errorf("Route53HostedZone lookup requires at least one of: id, name, tag")
	}
	if err != nil {
		return route53zone.ObservedState{}, err
	}
	if strings.TrimSpace(id) == "" {
		return route53zone.ObservedState{}, fmt.Errorf("not found")
	}
	return api.DescribeHostedZone(ctx, id)
}

func normalizeHostedZoneLookupName(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

func matchesHostedZoneFilter(observed route53zone.ObservedState, filter LookupFilter) bool {
	if strings.TrimSpace(filter.ID) != "" && observed.HostedZoneId != strings.TrimSpace(filter.ID) {
		return false
	}
	if strings.TrimSpace(filter.Name) != "" && observed.Name != normalizeHostedZoneLookupName(filter.Name) {
		return false
	}
	for key, value := range filter.Tag {
		if observed.Tags[key] != value {
			return false
		}
	}
	return true
}
