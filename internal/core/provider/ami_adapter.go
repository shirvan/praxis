// AMI provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + AMI name.
// AMIs are region-scoped; the key combines the AWS region with the image name.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/ami"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// AMIAdapter is the descriptor-driven adapter for AMI.
type AMIAdapter = GenericAdapter[ami.AMISpec, ami.AMIOutputs, ami.ObservedState]

// amiDescriptor builds the AMI descriptor. staticAPI carries the pre-built
// planning API (tests only); BuildImportKey uses it to resolve an AMI ID to
// the image name. Production adapters pass nil and import-by-ID fails with a
// configuration error, matching the original adapter's behavior.
func amiDescriptor(staticAPI ami.AMIAPI) GenericDescriptor[ami.AMISpec, ami.AMIOutputs, ami.ObservedState] {
	return GenericDescriptor[ami.AMISpec, ami.AMIOutputs, ami.ObservedState]{
		Kind:  ami.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (ami.AMISpec, error) {
			var parsed ami.AMISpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return ami.AMISpec{}, fmt.Errorf("decode AMI spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return ami.AMISpec{}, fmt.Errorf("AMI metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return ami.AMISpec{}, fmt.Errorf("AMI spec.region is required")
			}
			parsed.Name = name
			if err := amiValidateSource(parsed.Source); err != nil {
				return ami.AMISpec{}, err
			}
			if parsed.Tags == nil {
				parsed.Tags = make(map[string]string)
			}
			if parsed.Tags["Name"] == "" {
				parsed.Tags["Name"] = name
			}
			parsed.Account = ""
			return parsed, nil
		},

		KeyFromSpec: func(spec ami.AMISpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("AMI name", name); err != nil {
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
			if looksLikeAMIID(resourceID) {
				if staticAPI == nil {
					return "", fmt.Errorf("AMI adapter planning API is not configured for import key resolution")
				}
				obs, err := staticAPI.DescribeImage(context.Background(), resourceID)
				if err != nil {
					return "", fmt.Errorf("resolve AMI import key for %q: %w", resourceID, err)
				}
				if strings.TrimSpace(obs.Name) != "" {
					return JoinKey(region, obs.Name), nil
				}
			}
			return JoinKey(region, resourceID), nil
		},

		PrepareSpec: func(spec ami.AMISpec, key, account string) ami.AMISpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out ami.AMIOutputs) map[string]any {
			return map[string]any{
				"imageId":            out.ImageId,
				"name":               out.Name,
				"state":              out.State,
				"architecture":       out.Architecture,
				"virtualizationType": out.VirtualizationType,
				"rootDeviceName":     out.RootDeviceName,
				"ownerId":            out.OwnerId,
				"creationDate":       out.CreationDate,
			}
		},

		PlanID: func(out ami.AMIOutputs) string { return out.ImageId },

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[ami.ObservedState] {
			return amiProbe(ami.NewAMIAPI(awsclient.NewEC2Client(cfg)))
		},

		DiffFields: func(desired ami.AMISpec, observed ami.ObservedState) []types.FieldDiff {
			rawDiffs := ami.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// amiProbe adapts the driver API to the generic plan probe shape.
func amiProbe(api ami.AMIAPI) PlanProbeFunc[ami.ObservedState] {
	return func(runCtx restate.RunContext, imageID string) (ami.ObservedState, bool, error) {
		obs, err := api.DescribeImage(runCtx, imageID)
		if err != nil {
			if ami.IsNotFound(err) {
				return ami.ObservedState{}, false, nil
			}
			return ami.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewAMIAdapterWithAuth builds the production adapter; plan-time credentials
// are resolved through the Auth Service.
func NewAMIAdapterWithAuth(auth authservice.AuthClient) *AMIAdapter {
	return NewGenericAdapter(amiDescriptor(nil), auth)
}

// NewAMIAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewAMIAdapterWithAPI(api ami.AMIAPI) *AMIAdapter {
	return NewGenericAdapterWithProbe(amiDescriptor(api), amiProbe(api))
}

func amiValidateSource(source ami.SourceSpec) error {
	hasSnapshot := source.FromSnapshot != nil
	hasAMI := source.FromAMI != nil
	if !hasSnapshot && !hasAMI {
		return fmt.Errorf("exactly one of source.fromSnapshot or source.fromAMI must be specified")
	}
	if hasSnapshot && hasAMI {
		return fmt.Errorf("cannot specify both source.fromSnapshot and source.fromAMI")
	}
	if hasSnapshot && strings.TrimSpace(source.FromSnapshot.SnapshotId) == "" {
		return fmt.Errorf("AMI spec.source.fromSnapshot.snapshotId is required")
	}
	if hasAMI && strings.TrimSpace(source.FromAMI.SourceImageId) == "" {
		return fmt.Errorf("AMI spec.source.fromAMI.sourceImageId is required")
	}
	return nil
}

func looksLikeAMIID(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.HasPrefix(value, "ami-")
}
