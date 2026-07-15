// KeyPair provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + key pair name.
// Key pairs are region-scoped; the key combines the AWS region and the key pair name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/keypair"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// KeyPairAdapter is the descriptor-driven adapter for KeyPair.
type KeyPairAdapter = GenericAdapter[keypair.KeyPairSpec, keypair.KeyPairOutputs, keypair.ObservedState]

func keyPairDescriptor() GenericDescriptor[keypair.KeyPairSpec, keypair.KeyPairOutputs, keypair.ObservedState] {
	return GenericDescriptor[keypair.KeyPairSpec, keypair.KeyPairOutputs, keypair.ObservedState]{
		Kind:  keypair.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (keypair.KeyPairSpec, error) {
			var parsed keypair.KeyPairSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return keypair.KeyPairSpec{}, fmt.Errorf("decode KeyPair spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return keypair.KeyPairSpec{}, fmt.Errorf("KeyPair metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return keypair.KeyPairSpec{}, fmt.Errorf("KeyPair spec.region is required")
			}
			if parsed.KeyType == "" {
				parsed.KeyType = "ed25519"
			}
			if parsed.KeyType != "rsa" && parsed.KeyType != "ed25519" {
				return keypair.KeyPairSpec{}, fmt.Errorf("KeyPair spec.keyType must be \"rsa\" or \"ed25519\"")
			}
			if parsed.Tags == nil {
				parsed.Tags = make(map[string]string)
			}
			parsed.KeyName = name
			parsed.Account = ""
			return parsed, nil
		},

		KeyFromSpec: func(spec keypair.KeyPairSpec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("key pair name", spec.KeyName); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, spec.KeyName), nil
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

		PrepareSpec: func(spec keypair.KeyPairSpec, _ string, account string) keypair.KeyPairSpec {
			spec.Account = account
			return spec
		},

		NormalizeOutputs: func(out keypair.KeyPairOutputs) map[string]any {
			// PrivateKeyMaterial is deliberately excluded. Normalized outputs flow
			// into deployment state, resource-ready events, notification payloads,
			// and expression hydration, none of which are secret stores.
			return map[string]any{
				"keyName":        out.KeyName,
				"keyPairId":      out.KeyPairId,
				"keyFingerprint": out.KeyFingerprint,
				"keyType":        out.KeyType,
			}
		},

		PlanID: func(out keypair.KeyPairOutputs) string { return out.KeyName },

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[keypair.ObservedState] {
			return keyPairProbe(keypair.NewKeyPairAPI(awsclient.NewEC2Client(cfg)))
		},

		DiffFields: func(desired keypair.KeyPairSpec, observed keypair.ObservedState) []types.FieldDiff {
			rawDiffs := keypair.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// keyPairProbe adapts the driver API to the generic plan probe shape.
func keyPairProbe(api keypair.KeyPairAPI) PlanProbeFunc[keypair.ObservedState] {
	return func(runCtx restate.RunContext, keyName string) (keypair.ObservedState, bool, error) {
		obs, err := api.DescribeKeyPair(runCtx, keyName)
		if err != nil {
			if keypair.IsNotFound(err) {
				return keypair.ObservedState{}, false, nil
			}
			return keypair.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewKeyPairAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewKeyPairAdapterWithAuth(auth authservice.AuthClient) *KeyPairAdapter {
	return NewGenericAdapter(keyPairDescriptor(), auth)
}

// NewKeyPairAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewKeyPairAdapterWithAPI(api keypair.KeyPairAPI) *KeyPairAdapter {
	return NewGenericAdapterWithProbe(keyPairDescriptor(), keyPairProbe(api))
}
