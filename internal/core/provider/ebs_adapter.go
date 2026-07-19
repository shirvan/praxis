// EBSVolume provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + volume name.
// EBS volumes are region-scoped; the key combines the AWS region with the
// volume Name tag.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/ebs"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// EBSAdapter is the descriptor-driven adapter for EBSVolume, extended with
// per-kind default timeouts.
type EBSAdapter struct {
	*GenericAdapter[ebs.EBSVolumeSpec, ebs.EBSVolumeOutputs, ebs.ObservedState]
}

func ebsDescriptor() GenericDescriptor[ebs.EBSVolumeSpec, ebs.EBSVolumeOutputs, ebs.ObservedState] {
	return GenericDescriptor[ebs.EBSVolumeSpec, ebs.EBSVolumeOutputs, ebs.ObservedState]{
		Kind:  ebs.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (ebs.EBSVolumeSpec, error) {
			var parsed ebs.EBSVolumeSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return ebs.EBSVolumeSpec{}, fmt.Errorf("decode EBSVolume spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return ebs.EBSVolumeSpec{}, fmt.Errorf("EBSVolume metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return ebs.EBSVolumeSpec{}, fmt.Errorf("EBSVolume spec.region is required")
			}
			if strings.TrimSpace(parsed.AvailabilityZone) == "" {
				return ebs.EBSVolumeSpec{}, fmt.Errorf("EBSVolume spec.availabilityZone is required")
			}
			if parsed.Tags == nil {
				parsed.Tags = make(map[string]string)
			}
			if parsed.Tags["Name"] == "" {
				parsed.Tags["Name"] = name
			}
			if parsed.VolumeType == "" {
				parsed.VolumeType = "gp3"
			}
			if parsed.SizeGiB == 0 {
				parsed.SizeGiB = 20
			}
			parsed.Account = ""
			return parsed, nil
		},

		KeyFromSpec: func(spec ebs.EBSVolumeSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("volume name", name); err != nil {
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

		PrepareSpec: func(spec ebs.EBSVolumeSpec, key, account string) ebs.EBSVolumeSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out ebs.EBSVolumeOutputs) map[string]any {
			result := map[string]any{
				"volumeId":         out.VolumeId,
				"availabilityZone": out.AvailabilityZone,
				"state":            out.State,
				"sizeGiB":          out.SizeGiB,
				"volumeType":       out.VolumeType,
				"encrypted":        out.Encrypted,
			}
			if out.ARN != "" {
				result["arn"] = out.ARN
			}
			return result
		},

		PlanIdentity: storedPlanIdentity[ebs.EBSVolumeSpec](func(out ebs.EBSVolumeOutputs) string { return out.VolumeId }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[ebs.EBSVolumeSpec, ebs.EBSVolumeOutputs, ebs.ObservedState] {
			return ebsProbe(ebs.NewEBSAPI(awsclient.NewEC2Client(cfg)))
		},

		DiffFields: func(desired ebs.EBSVolumeSpec, observed ebs.ObservedState, _ ebs.EBSVolumeOutputs) []types.FieldDiff {
			rawDiffs := ebs.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// ebsProbe adapts the driver API to the generic plan probe shape.
func ebsProbe(api ebs.EBSAPI) PlanProbeFunc[ebs.EBSVolumeSpec, ebs.EBSVolumeOutputs, ebs.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[ebs.EBSVolumeSpec, ebs.EBSVolumeOutputs]) (ebs.ObservedState, bool, error) {
		volumeID := input.Identity
		obs, err := api.DescribeVolume(runCtx, volumeID)
		if err != nil {
			if ebs.IsNotFound(err) {
				return ebs.ObservedState{}, false, nil
			}
			return ebs.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewEBSAdapterWithAuth builds the production adapter; plan-time credentials
// are resolved through the Auth Service.
func NewEBSAdapterWithAuth(auth authservice.AuthClient) *EBSAdapter {
	return &EBSAdapter{GenericAdapter: NewGenericAdapter(ebsDescriptor(), auth)}
}

// NewEBSAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewEBSAdapterWithAPI(api ebs.EBSAPI) *EBSAdapter {
	return &EBSAdapter{GenericAdapter: NewGenericAdapterWithProbe(ebsDescriptor(), ebsProbe(api))}
}

// DefaultTimeouts provides per-kind default timeouts for EBS volumes.
func (a *EBSAdapter) DefaultTimeouts() types.ResourceTimeouts {
	return types.ResourceTimeouts{Create: "10m", Update: "10m", Delete: "10m"}
}
