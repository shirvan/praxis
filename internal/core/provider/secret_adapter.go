// SecretsManagerSecret provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + secret name.
// Secret names are unique within a region; the key combines the AWS region and
// the secret name (metadata.name).
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/secret"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// SecretsManagerSecretAdapter is the descriptor-driven adapter for SecretsManagerSecret.
type SecretsManagerSecretAdapter = GenericAdapter[secret.SecretsManagerSecretSpec, secret.SecretsManagerSecretOutputs, secret.ObservedState]

func secretsManagerSecretDescriptor() GenericDescriptor[secret.SecretsManagerSecretSpec, secret.SecretsManagerSecretOutputs, secret.ObservedState] {
	return GenericDescriptor[secret.SecretsManagerSecretSpec, secret.SecretsManagerSecretOutputs, secret.ObservedState]{
		Kind:  secret.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (secret.SecretsManagerSecretSpec, error) {
			var parsed secret.SecretsManagerSecretSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return secret.SecretsManagerSecretSpec{}, fmt.Errorf("decode SecretsManagerSecret spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return secret.SecretsManagerSecretSpec{}, fmt.Errorf("SecretsManagerSecret metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return secret.SecretsManagerSecretSpec{}, fmt.Errorf("SecretsManagerSecret spec.region is required")
			}
			if parsed.Tags == nil {
				parsed.Tags = map[string]string{}
			}
			parsed.Name = name
			return parsed, nil
		},

		KeyFromSpec: func(spec secret.SecretsManagerSecretSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("secret name", name); err != nil {
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

		PrepareSpec: func(spec secret.SecretsManagerSecretSpec, key, account string) secret.SecretsManagerSecretSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out secret.SecretsManagerSecretOutputs) map[string]any {
			result := map[string]any{
				"name": out.Name,
			}
			if out.ARN != "" {
				result["arn"] = out.ARN
			}
			if out.VersionID != "" {
				result["versionId"] = out.VersionID
			}
			return result
		},

		PlanIdentity: storedPlanIdentity[secret.SecretsManagerSecretSpec](func(out secret.SecretsManagerSecretOutputs) string { return out.Name }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[secret.SecretsManagerSecretSpec, secret.SecretsManagerSecretOutputs, secret.ObservedState] {
			return secretsManagerSecretProbe(secret.NewSecretsManagerSecretAPI(awsclient.NewSecretsManagerClient(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[secret.SecretsManagerSecretOutputs] {
			return secretsManagerSecretLookupProbe(secret.NewSecretsManagerSecretMetadataAPI(awsclient.NewSecretsManagerClient(cfg)))
		},

		DiffFields: func(desired secret.SecretsManagerSecretSpec, observed secret.ObservedState, _ secret.SecretsManagerSecretOutputs) []types.FieldDiff {
			return secret.ComputeFieldDiffs(desired, observed)
		},
		SensitiveFields: []string{"spec.secretString"},
	}
}

func secretsManagerSecretLookupProbe(api secret.SecretsManagerSecretMetadataAPI) LookupProbeFunc[secret.SecretsManagerSecretOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (secret.SecretsManagerSecretOutputs, bool, error) {
		identity := nativeLookupIdentity(filter)
		if identity == "" {
			return secret.SecretsManagerSecretOutputs{}, false, restate.TerminalError(fmt.Errorf("SecretsManagerSecret lookup supports id or name; tag-only lookup is not available"), 400)
		}
		observed, found, err := api.DescribeSecretMetadata(ctx, identity)
		if err != nil {
			if isLookupNotFound(err, secret.IsNotFound) {
				return secret.SecretsManagerSecretOutputs{}, false, nil
			}
			return secret.SecretsManagerSecretOutputs{}, false, err
		}
		if !found {
			return secret.SecretsManagerSecretOutputs{}, false, nil
		}
		if id := strings.TrimSpace(filter.ID); id != "" && observed.ARN != id && observed.Name != id {
			return secret.SecretsManagerSecretOutputs{}, false, nil
		}
		if name := strings.TrimSpace(filter.Name); name != "" && observed.Name != name {
			return secret.SecretsManagerSecretOutputs{}, false, nil
		}
		if !matchesLookupTags(observed.Tags, LookupFilter{Tag: filter.Tag}) {
			return secret.SecretsManagerSecretOutputs{}, false, nil
		}
		return secret.SecretsManagerSecretOutputs{ARN: observed.ARN, Name: observed.Name, VersionID: observed.VersionID}, true, nil
	}
}

// secretsManagerSecretProbe adapts the driver API to the generic plan probe shape.
func secretsManagerSecretProbe(api secret.SecretsManagerSecretAPI) PlanProbeFunc[secret.SecretsManagerSecretSpec, secret.SecretsManagerSecretOutputs, secret.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[secret.SecretsManagerSecretSpec, secret.SecretsManagerSecretOutputs]) (secret.ObservedState, bool, error) {
		name := input.Identity
		obs, found, err := api.DescribeSecret(runCtx, name)
		if err != nil {
			if secret.IsNotFound(err) {
				return secret.ObservedState{}, false, nil
			}
			return secret.ObservedState{}, false, err
		}
		return obs, found, nil
	}
}

// NewSecretsManagerSecretAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewSecretsManagerSecretAdapterWithAuth(auth authservice.AuthClient) *SecretsManagerSecretAdapter {
	return NewGenericAdapter(secretsManagerSecretDescriptor(), auth)
}

// NewSecretsManagerSecretAdapterWithAPI builds an adapter with a fixed planning
// API. Used by tests.
func NewSecretsManagerSecretAdapterWithAPI(api secret.SecretsManagerSecretAPI) *SecretsManagerSecretAdapter {
	if metadataAPI, ok := api.(secret.SecretsManagerSecretMetadataAPI); ok {
		return NewGenericAdapterWithProbes(secretsManagerSecretDescriptor(), secretsManagerSecretProbe(api), secretsManagerSecretLookupProbe(metadataAPI))
	}
	return NewGenericAdapterWithProbe(secretsManagerSecretDescriptor(), secretsManagerSecretProbe(api))
}
