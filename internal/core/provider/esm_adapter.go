// EventSourceMapping provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + function name + encoded event source key.
// Event source mappings are region-scoped; the key combines region, function
// name, and an encoded event source identifier.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/esm"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// ESMAdapter is the descriptor-driven adapter for EventSourceMapping.
type ESMAdapter = GenericAdapter[esm.EventSourceMappingSpec, esm.EventSourceMappingOutputs, esm.ObservedState]

func esmDescriptor() GenericDescriptor[esm.EventSourceMappingSpec, esm.EventSourceMappingOutputs, esm.ObservedState] {
	return GenericDescriptor[esm.EventSourceMappingSpec, esm.EventSourceMappingOutputs, esm.ObservedState]{
		Kind:  esm.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(rawSpec json.RawMessage, _ string) (esm.EventSourceMappingSpec, error) {
			var spec esm.EventSourceMappingSpec
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return esm.EventSourceMappingSpec{}, fmt.Errorf("decode EventSourceMapping spec: %w", err)
			}
			if strings.TrimSpace(spec.Region) == "" {
				return esm.EventSourceMappingSpec{}, fmt.Errorf("EventSourceMapping spec.region is required")
			}
			return spec, nil
		},

		KeyFromSpec: func(spec esm.EventSourceMappingSpec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("function name", spec.FunctionName); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, spec.FunctionName, esm.EncodedEventSourceKey(spec.EventSourceArn)), nil
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

		PrepareSpec: func(spec esm.EventSourceMappingSpec, key, account string) esm.EventSourceMappingSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out esm.EventSourceMappingOutputs) map[string]any {
			return map[string]any{
				"uuid":           out.UUID,
				"eventSourceArn": out.EventSourceArn,
				"functionArn":    out.FunctionArn,
				"state":          out.State,
				"lastModified":   out.LastModified,
				"batchSize":      out.BatchSize,
			}
		},

		PlanIdentity: storedPlanIdentity[esm.EventSourceMappingSpec](func(out esm.EventSourceMappingOutputs) string { return out.UUID }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[esm.EventSourceMappingSpec, esm.EventSourceMappingOutputs, esm.ObservedState] {
			return esmProbe(esm.NewESMAPI(awsclient.NewLambdaClient(cfg)))
		},

		DiffFields: func(desired esm.EventSourceMappingSpec, observed esm.ObservedState, _ esm.EventSourceMappingOutputs) []types.FieldDiff {
			return esm.ComputeFieldDiffs(desired, observed)
		},
	}
}

// esmProbe adapts the driver API to the generic plan probe shape.
func esmProbe(api esm.ESMAPI) PlanProbeFunc[esm.EventSourceMappingSpec, esm.EventSourceMappingOutputs, esm.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[esm.EventSourceMappingSpec, esm.EventSourceMappingOutputs]) (esm.ObservedState, bool, error) {
		uuid := input.Identity
		obs, err := api.GetEventSourceMapping(runCtx, uuid)
		if err != nil {
			if esm.IsNotFound(err) {
				return esm.ObservedState{}, false, nil
			}
			return esm.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewESMAdapterWithAuth builds the production adapter; plan-time credentials
// are resolved through the Auth Service.
func NewESMAdapterWithAuth(auth authservice.AuthClient) *ESMAdapter {
	return NewGenericAdapter(esmDescriptor(), auth)
}

// NewESMAdapterWithAPI builds an adapter with a fixed planning API. Used by tests.
func NewESMAdapterWithAPI(api esm.ESMAPI) *ESMAdapter {
	return NewGenericAdapterWithProbe(esmDescriptor(), esmProbe(api))
}
