// Route53Record provider adapter — descriptor for the GenericAdapter.
//
// Key scope: custom.
// Key parts: zone ID + record name + record type (+ optional set identifier).
// Route 53 records are scoped to a hosted zone; the key combines zone ID,
// record name, and record type.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/route53record"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// Route53RecordAdapter is the descriptor-driven adapter for Route53Record.
type Route53RecordAdapter = GenericAdapter[route53record.RecordSpec, route53record.RecordOutputs, route53record.ObservedState]

func route53RecordDescriptor() GenericDescriptor[route53record.RecordSpec, route53record.RecordOutputs, route53record.ObservedState] {
	return GenericDescriptor[route53record.RecordSpec, route53record.RecordOutputs, route53record.ObservedState]{
		Kind:  route53record.ServiceName,
		Scope: KeyScopeCustom,

		DecodeSpec: func(rawSpec json.RawMessage, _ string) (route53record.RecordSpec, error) {
			var spec route53record.RecordSpec
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return route53record.RecordSpec{}, fmt.Errorf("decode Route53Record spec: %w", err)
			}
			spec.HostedZoneId = strings.TrimSpace(strings.TrimPrefix(spec.HostedZoneId, "/hostedzone/"))
			spec.Name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(spec.Name), "."))
			spec.Type = strings.ToUpper(strings.TrimSpace(spec.Type))
			spec.SetIdentifier = strings.TrimSpace(spec.SetIdentifier)
			// Only the orchestrator (not the template author) may set the account.
			spec.Account = ""
			return spec, nil
		},

		KeyFromSpec: func(spec route53record.RecordSpec, _ string) (string, error) {
			if err := ValidateKeyPart("hosted zone ID", spec.HostedZoneId); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("record name", spec.Name); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("record type", spec.Type); err != nil {
				return "", err
			}
			parts := []string{spec.HostedZoneId, spec.Name, spec.Type}
			if spec.SetIdentifier != "" {
				if err := ValidateKeyPart("record set identifier", spec.SetIdentifier); err != nil {
					return "", err
				}
				parts = append(parts, spec.SetIdentifier)
			}
			return JoinKey(parts...), nil
		},

		ImportKey: func(_, resourceID string) (string, error) {
			trimmed := strings.TrimSpace(resourceID)
			if trimmed == "" {
				return "", fmt.Errorf("resource ID is required to build a resource key")
			}
			parts := strings.Split(trimmed, "/")
			if len(parts) == 1 {
				parts = strings.Split(trimmed, "~")
			}
			if len(parts) < 3 || len(parts) > 4 {
				return "", fmt.Errorf("Route53Record import resource ID must be <hostedZoneId>/<fqdn>/<type>[/<setIdentifier>]")
			}
			return JoinKey(parts...), nil
		},

		PrepareSpec: func(spec route53record.RecordSpec, key, account string) route53record.RecordSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out route53record.RecordOutputs) map[string]any {
			result := map[string]any{"hostedZoneId": out.HostedZoneId, "fqdn": out.FQDN, "type": out.Type}
			if out.SetIdentifier != "" {
				result["setIdentifier"] = out.SetIdentifier
			}
			return result
		},

		// The plan ID packs the record identity (zone ID, FQDN, type, optional
		// set identifier) into one key-shaped string; the probe unpacks it.
		PlanID: func(out route53record.RecordOutputs) string {
			if out.FQDN == "" {
				return ""
			}
			parts := []string{out.HostedZoneId, out.FQDN, out.Type}
			if out.SetIdentifier != "" {
				parts = append(parts, out.SetIdentifier)
			}
			return JoinKey(parts...)
		},

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[route53record.ObservedState] {
			return route53RecordProbe(route53record.NewRecordAPI(awsclient.NewRoute53Client(cfg)))
		},

		DiffFields: func(desired route53record.RecordSpec, observed route53record.ObservedState) []types.FieldDiff {
			rawDiffs := route53record.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// route53RecordProbe adapts the driver API to the generic plan probe shape.
// The plan ID carries the packed record identity produced by PlanID.
func route53RecordProbe(api route53record.RecordAPI) PlanProbeFunc[route53record.ObservedState] {
	return func(runCtx restate.RunContext, planID string) (route53record.ObservedState, bool, error) {
		parts := strings.Split(planID, KeySeparator)
		if len(parts) < 3 {
			return route53record.ObservedState{}, false, fmt.Errorf("Route53Record plan ID %q must be <hostedZoneId>~<fqdn>~<type>[~<setIdentifier>]", planID)
		}
		identity := route53record.RecordIdentity{HostedZoneId: parts[0], Name: parts[1], Type: parts[2]}
		if len(parts) > 3 {
			identity.SetIdentifier = parts[3]
		}
		obs, err := api.DescribeRecord(runCtx, identity)
		if err != nil {
			if route53record.IsNotFound(err) {
				return route53record.ObservedState{}, false, nil
			}
			return route53record.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewRoute53RecordAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewRoute53RecordAdapterWithAuth(auth authservice.AuthClient) *Route53RecordAdapter {
	return NewGenericAdapter(route53RecordDescriptor(), auth)
}

// NewRoute53RecordAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewRoute53RecordAdapterWithAPI(api route53record.RecordAPI) *Route53RecordAdapter {
	return NewGenericAdapterWithProbe(route53RecordDescriptor(), route53RecordProbe(api))
}
