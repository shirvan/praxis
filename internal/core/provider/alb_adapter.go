// ALB provider adapter — descriptor for the GenericAdapter.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/alb"
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type ALBAdapter struct {
	*GenericAdapter[alb.ALBSpec, alb.ALBOutputs, alb.ObservedState]
}

func albDescriptor() GenericDescriptor[alb.ALBSpec, alb.ALBOutputs, alb.ObservedState] {
	return GenericDescriptor[alb.ALBSpec, alb.ALBOutputs, alb.ObservedState]{
		Kind:  alb.ServiceName,
		Scope: KeyScopeRegion,
		DecodeSpec: func(raw json.RawMessage, metadataName string) (alb.ALBSpec, error) {
			var spec alb.ALBSpec
			if err := json.Unmarshal(raw, &spec); err != nil {
				return alb.ALBSpec{}, fmt.Errorf("decode ALB spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return alb.ALBSpec{}, fmt.Errorf("ALB metadata.name is required")
			}
			if strings.TrimSpace(spec.Region) == "" {
				return alb.ALBSpec{}, fmt.Errorf("ALB spec.region is required")
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
		KeyFromSpec: func(spec alb.ALBSpec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("ALB name", spec.Name); err != nil {
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
		PrepareSpec: func(spec alb.ALBSpec, _ string, account string) alb.ALBSpec {
			spec.Account = account
			return spec
		},
		NormalizeOutputs: func(out alb.ALBOutputs) map[string]any {
			return map[string]any{
				"loadBalancerArn":       out.LoadBalancerArn,
				"dnsName":               out.DnsName,
				"hostedZoneId":          out.HostedZoneId,
				"vpcId":                 out.VpcId,
				"canonicalHostedZoneId": out.CanonicalHostedZoneId,
			}
		},
		PlanIdentity: func(desired alb.ALBSpec, outputs alb.ALBOutputs) (string, bool) {
			if outputs.LoadBalancerArn != "" {
				return outputs.LoadBalancerArn, true
			}
			return desired.Name, desired.Name != ""
		},
		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[alb.ALBSpec, alb.ALBOutputs, alb.ObservedState] {
			return albProbe(alb.NewALBAPI(awsclient.NewELBv2Client(cfg)))
		},
		DiffFields: func(desired alb.ALBSpec, observed alb.ObservedState, _ alb.ALBOutputs) []types.FieldDiff {
			raw := alb.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(raw))
			for _, diff := range raw {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

func albProbe(api alb.ALBAPI) PlanProbeFunc[alb.ALBSpec, alb.ALBOutputs, alb.ObservedState] {
	return func(ctx restate.RunContext, input PlanProbeInput[alb.ALBSpec, alb.ALBOutputs]) (alb.ObservedState, bool, error) {
		observed, err := api.DescribeALB(ctx, input.Identity)
		if err != nil {
			if alb.IsNotFound(err) {
				return alb.ObservedState{}, false, nil
			}
			if awserr.IsThrottled(err) {
				return alb.ObservedState{}, false, err
			}
			return alb.ObservedState{}, false, restate.TerminalError(err, 500)
		}
		return observed, true, nil
	}
}

func NewALBAdapterWithAuth(auth authservice.AuthClient) *ALBAdapter {
	return &ALBAdapter{GenericAdapter: NewGenericAdapter(albDescriptor(), auth)}
}

func NewALBAdapterWithAPI(api alb.ALBAPI) *ALBAdapter {
	return &ALBAdapter{GenericAdapter: NewGenericAdapterWithProbe(albDescriptor(), albProbe(api))}
}

func (a *ALBAdapter) DefaultTimeouts() types.ResourceTimeouts {
	return types.ResourceTimeouts{Create: "10m", Update: "10m", Delete: "10m"}
}
