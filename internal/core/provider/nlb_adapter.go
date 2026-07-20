// NLB provider adapter — descriptor for the GenericAdapter.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/nlb"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type NLBAdapter struct {
	*GenericAdapter[nlb.NLBSpec, nlb.NLBOutputs, nlb.ObservedState]
}

func nlbDescriptor() GenericDescriptor[nlb.NLBSpec, nlb.NLBOutputs, nlb.ObservedState] {
	return GenericDescriptor[nlb.NLBSpec, nlb.NLBOutputs, nlb.ObservedState]{
		Kind:  nlb.ServiceName,
		Scope: KeyScopeRegion,
		DecodeSpec: func(raw json.RawMessage, metadataName string) (nlb.NLBSpec, error) {
			var spec nlb.NLBSpec
			if err := json.Unmarshal(raw, &spec); err != nil {
				return nlb.NLBSpec{}, fmt.Errorf("decode NLB spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return nlb.NLBSpec{}, fmt.Errorf("NLB metadata.name is required")
			}
			if strings.TrimSpace(spec.Region) == "" {
				return nlb.NLBSpec{}, fmt.Errorf("NLB spec.region is required")
			}
			spec.Name = name
			spec.Account = ""
			if spec.Tags == nil {
				spec.Tags = map[string]string{}
			}
			if spec.Tags["Name"] == "" {
				spec.Tags["Name"] = name
			}
			return spec, nil
		},
		KeyFromSpec: func(spec nlb.NLBSpec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("NLB name", spec.Name); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, spec.Name), nil
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
		PrepareSpec: func(spec nlb.NLBSpec, _ string, account string) nlb.NLBSpec {
			spec.Account = account
			return spec
		},
		NormalizeOutputs: func(out nlb.NLBOutputs) map[string]any {
			return map[string]any{
				"loadBalancerArn":       out.LoadBalancerArn,
				"dnsName":               out.DnsName,
				"hostedZoneId":          out.HostedZoneId,
				"vpcId":                 out.VpcId,
				"canonicalHostedZoneId": out.CanonicalHostedZoneId,
			}
		},
		PlanIdentity: func(desired nlb.NLBSpec, outputs nlb.NLBOutputs) (string, bool) {
			if outputs.LoadBalancerArn != "" {
				return outputs.LoadBalancerArn, true
			}
			return desired.Name, desired.Name != ""
		},
		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[nlb.NLBSpec, nlb.NLBOutputs, nlb.ObservedState] {
			return nlbProbe(nlb.NewNLBAPI(awsclient.NewELBv2Client(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[nlb.NLBOutputs] {
			return nlbLookupProbe(nlb.NewNLBAPI(awsclient.NewELBv2Client(cfg)))
		},
		DiffFields: func(desired nlb.NLBSpec, observed nlb.ObservedState, _ nlb.NLBOutputs) []types.FieldDiff {
			return nlb.ComputeFieldDiffs(desired, observed)
		},
	}
}

func nlbLookupProbe(api nlb.NLBAPI) LookupProbeFunc[nlb.NLBOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (nlb.NLBOutputs, bool, error) {
		identity := nativeLookupIdentity(filter)
		if identity == "" {
			return nlb.NLBOutputs{}, false, restate.TerminalError(
				fmt.Errorf("NLB lookup supports id or name; tag-only lookup is not available"),
				400,
			)
		}
		observed, err := api.DescribeNLB(ctx, identity)
		if err != nil {
			if isLookupNotFound(err, nlb.IsNotFound) {
				return nlb.NLBOutputs{}, false, nil
			}
			return nlb.NLBOutputs{}, false, err
		}
		if id := strings.TrimSpace(filter.ID); id != "" && observed.LoadBalancerArn != id {
			return nlb.NLBOutputs{}, false, nil
		}
		if name := strings.TrimSpace(filter.Name); name != "" && observed.Name != name {
			return nlb.NLBOutputs{}, false, nil
		}
		if !matchesLookupTags(observed.Tags, LookupFilter{Tag: filter.Tag}) {
			return nlb.NLBOutputs{}, false, nil
		}
		return nlb.NLBOutputs{
			LoadBalancerArn:       observed.LoadBalancerArn,
			DnsName:               observed.DnsName,
			HostedZoneId:          observed.HostedZoneId,
			VpcId:                 observed.VpcId,
			CanonicalHostedZoneId: observed.HostedZoneId,
		}, true, nil
	}
}

func nlbProbe(api nlb.NLBAPI) PlanProbeFunc[nlb.NLBSpec, nlb.NLBOutputs, nlb.ObservedState] {
	return func(ctx restate.RunContext, input PlanProbeInput[nlb.NLBSpec, nlb.NLBOutputs]) (nlb.ObservedState, bool, error) {
		observed, err := api.DescribeNLB(ctx, input.Identity)
		if err != nil {
			if nlb.IsNotFound(err) {
				return nlb.ObservedState{}, false, nil
			}
			return nlb.ObservedState{}, false, err
		}
		return observed, true, nil
	}
}

func NewNLBAdapterWithAuth(auth authservice.AuthClient) *NLBAdapter {
	return &NLBAdapter{GenericAdapter: NewGenericAdapter(nlbDescriptor(), auth)}
}

func NewNLBAdapterWithAPI(api nlb.NLBAPI) *NLBAdapter {
	return &NLBAdapter{GenericAdapter: NewGenericAdapterWithProbes(nlbDescriptor(), nlbProbe(api), nlbLookupProbe(api))}
}

func (a *NLBAdapter) DefaultTimeouts() types.ResourceTimeouts {
	return types.ResourceTimeouts{Create: "10m", Update: "10m", Delete: "10m"}
}
