// KMSKey provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + alias short name (metadata.name, without "alias/").
// Alias names are unique within a region; the key combines the AWS region and
// the alias short name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/kmskey"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// KMSKeyAdapter is the descriptor-driven adapter for KMSKey.
type KMSKeyAdapter = GenericAdapter[kmskey.KMSKeySpec, kmskey.KMSKeyOutputs, kmskey.ObservedState]

func kmsKeyDescriptor() GenericDescriptor[kmskey.KMSKeySpec, kmskey.KMSKeyOutputs, kmskey.ObservedState] {
	return GenericDescriptor[kmskey.KMSKeySpec, kmskey.KMSKeyOutputs, kmskey.ObservedState]{
		Kind:  kmskey.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (kmskey.KMSKeySpec, error) {
			var parsed kmskey.KMSKeySpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return kmskey.KMSKeySpec{}, fmt.Errorf("decode KMSKey spec: %w", err)
			}
			name := aliasShortName(metadataName)
			if name == "" {
				return kmskey.KMSKeySpec{}, fmt.Errorf("KMSKey metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return kmskey.KMSKeySpec{}, fmt.Errorf("KMSKey spec.region is required")
			}
			if parsed.Tags == nil {
				parsed.Tags = map[string]string{}
			}
			parsed.Name = name
			return parsed, nil
		},

		KeyFromSpec: func(spec kmskey.KMSKeySpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := aliasShortName(metadataName)
			if err := ValidateKeyPart("alias name", name); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, name), nil
		},

		ImportKey: func(region, resourceID string) (string, error) {
			if err := ValidateKeyPart("region", region); err != nil {
				return "", err
			}
			name := aliasShortName(resourceID)
			if err := ValidateKeyPart("resource ID", name); err != nil {
				return "", err
			}
			return JoinKey(region, name), nil
		},

		PrepareSpec: func(spec kmskey.KMSKeySpec, key, account string) kmskey.KMSKeySpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out kmskey.KMSKeyOutputs) map[string]any {
			result := map[string]any{}
			if out.ARN != "" {
				result["arn"] = out.ARN
			}
			if out.KeyID != "" {
				result["keyId"] = out.KeyID
			}
			if out.AliasName != "" {
				result["aliasName"] = out.AliasName
			}
			return result
		},

		PlanIdentity: storedPlanIdentity[kmskey.KMSKeySpec](func(out kmskey.KMSKeyOutputs) string { return out.AliasName }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[kmskey.KMSKeySpec, kmskey.KMSKeyOutputs, kmskey.ObservedState] {
			return kmsKeyProbe(kmskey.NewKMSKeyAPI(awsclient.NewKMSClient(cfg)))
		},

		DiffFields: func(desired kmskey.KMSKeySpec, observed kmskey.ObservedState, _ kmskey.KMSKeyOutputs) []types.FieldDiff {
			rawDiffs := kmskey.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// aliasShortName strips a leading "alias/" and surrounding whitespace so that
// keys are always built from the bare alias short name.
func aliasShortName(name string) string {
	return strings.TrimPrefix(strings.TrimSpace(name), "alias/")
}

// kmsKeyProbe adapts the driver API to the generic plan probe shape. The plan ID
// is the full alias ("alias/<name>"), which DescribeKey resolves directly.
func kmsKeyProbe(api kmskey.KMSKeyAPI) PlanProbeFunc[kmskey.KMSKeySpec, kmskey.KMSKeyOutputs, kmskey.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[kmskey.KMSKeySpec, kmskey.KMSKeyOutputs]) (kmskey.ObservedState, bool, error) {
		alias := input.Identity
		obs, found, err := api.DescribeKey(runCtx, alias)
		if err != nil {
			if kmskey.IsNotFound(err) {
				return kmskey.ObservedState{}, false, nil
			}
			return kmskey.ObservedState{}, false, err
		}
		return obs, found, nil
	}
}

// NewKMSKeyAdapterWithAuth builds the production adapter; plan-time credentials
// are resolved through the Auth Service.
func NewKMSKeyAdapterWithAuth(auth authservice.AuthClient) *KMSKeyAdapter {
	return NewGenericAdapter(kmsKeyDescriptor(), auth)
}

// NewKMSKeyAdapterWithAPI builds an adapter with a fixed planning API. Used by tests.
func NewKMSKeyAdapterWithAPI(api kmskey.KMSKeyAPI) *KMSKeyAdapter {
	return NewGenericAdapterWithProbe(kmsKeyDescriptor(), kmsKeyProbe(api))
}
