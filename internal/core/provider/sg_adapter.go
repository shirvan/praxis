// SecurityGroup provider adapter — generic lifecycle plus lookup/observe capabilities.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/sg"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type SecurityGroupAdapter struct {
	*GenericAdapter[sg.SecurityGroupSpec, sg.SecurityGroupOutputs, sg.ObservedState]
	auth       authservice.AuthClient
	staticAPI  sg.SGAPI
	apiFactory func(aws.Config) sg.SGAPI
}

func securityGroupDescriptor() GenericDescriptor[sg.SecurityGroupSpec, sg.SecurityGroupOutputs, sg.ObservedState] {
	return GenericDescriptor[sg.SecurityGroupSpec, sg.SecurityGroupOutputs, sg.ObservedState]{
		Kind:  sg.ServiceName,
		Scope: KeyScopeCustom,
		DecodeSpec: func(raw json.RawMessage, _ string) (sg.SecurityGroupSpec, error) {
			var spec sg.SecurityGroupSpec
			if err := json.Unmarshal(raw, &spec); err != nil {
				return sg.SecurityGroupSpec{}, fmt.Errorf("decode SecurityGroup spec: %w", err)
			}
			if strings.TrimSpace(spec.GroupName) == "" {
				return sg.SecurityGroupSpec{}, fmt.Errorf("SecurityGroup spec.groupName is required")
			}
			spec.Account = ""
			return spec, nil
		},
		KeyFromSpec: func(spec sg.SecurityGroupSpec, _ string) (string, error) {
			if err := ValidateKeyPart("VPC ID", spec.VpcId); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("group name", spec.GroupName); err != nil {
				return "", err
			}
			return JoinKey(spec.VpcId, spec.GroupName), nil
		},
		ImportKey: func(_ string, resourceID string) (string, error) {
			if err := ValidateKeyPart("resource ID", resourceID); err != nil {
				return "", err
			}
			return resourceID, nil
		},
		PrepareSpec: func(spec sg.SecurityGroupSpec, _ string, account string) sg.SecurityGroupSpec {
			spec.Account = account
			return spec
		},
		NormalizeOutputs: func(out sg.SecurityGroupOutputs) map[string]any {
			return map[string]any{"groupId": out.GroupId, "groupArn": out.GroupArn, "vpcId": out.VpcId}
		},
		PlanIdentity: func(desired sg.SecurityGroupSpec, _ sg.SecurityGroupOutputs) (string, bool) {
			return desired.GroupName, desired.GroupName != "" && desired.VpcId != ""
		},
		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[sg.SecurityGroupSpec, sg.SecurityGroupOutputs, sg.ObservedState] {
			return securityGroupProbe(sg.NewSGAPI(awsclient.NewEC2Client(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[sg.SecurityGroupOutputs] {
			return securityGroupLookupProbe(sg.NewSGAPI(awsclient.NewEC2Client(cfg)))
		},
		DiffFields: func(desired sg.SecurityGroupSpec, observed sg.ObservedState, _ sg.SecurityGroupOutputs) []types.FieldDiff {
			return sg.ComputeFieldDiffs(desired, observed)
		},
	}
}

func securityGroupProbe(api sg.SGAPI) PlanProbeFunc[sg.SecurityGroupSpec, sg.SecurityGroupOutputs, sg.ObservedState] {
	return func(ctx restate.RunContext, input PlanProbeInput[sg.SecurityGroupSpec, sg.SecurityGroupOutputs]) (sg.ObservedState, bool, error) {
		observed, err := api.FindSecurityGroup(ctx, input.Identity, input.Desired.VpcId)
		if err != nil {
			if sg.IsNotFound(err) {
				return sg.ObservedState{}, false, nil
			}
			return sg.ObservedState{}, false, err
		}
		return observed, true, nil
	}
}

func securityGroupLookupProbe(api sg.SGAPI) LookupProbeFunc[sg.SecurityGroupOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (sg.SecurityGroupOutputs, bool, error) {
		observed, err := lookupSecurityGroup(ctx, api, filter)
		if err != nil {
			if isLookupNotFound(err, sg.IsNotFound) {
				return sg.SecurityGroupOutputs{}, false, nil
			}
			return sg.SecurityGroupOutputs{}, false, err
		}
		if !matchesSecurityGroupFilter(observed, filter) {
			return sg.SecurityGroupOutputs{}, false, nil
		}
		return sg.SecurityGroupOutputs{
			GroupId:  observed.GroupId,
			GroupArn: securityGroupARN(filter.Region, observed.OwnerId, observed.GroupId),
			VpcId:    observed.VpcId,
		}, true, nil
	}
}

func NewSecurityGroupAdapterWithAuth(auth authservice.AuthClient) *SecurityGroupAdapter {
	factory := func(cfg aws.Config) sg.SGAPI { return sg.NewSGAPI(awsclient.NewEC2Client(cfg)) }
	return &SecurityGroupAdapter{
		GenericAdapter: NewGenericAdapter(securityGroupDescriptor(), auth),
		auth:           auth,
		apiFactory:     factory,
	}
}

func NewSecurityGroupAdapterWithAPI(api sg.SGAPI) *SecurityGroupAdapter {
	return &SecurityGroupAdapter{
		GenericAdapter: NewGenericAdapterWithProbes(securityGroupDescriptor(), securityGroupProbe(api), securityGroupLookupProbe(api)),
		staticAPI:      api,
	}
}

func (a *SecurityGroupAdapter) Observe(ctx restate.Context, key string, account string, spec any) (ObserveResult, error) {
	desired, err := castSpec[sg.SecurityGroupSpec](spec)
	if err != nil {
		return ObserveResult{}, err
	}
	outputs, getErr := restate.Object[sg.SecurityGroupOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil || outputs.GroupId == "" {
		return ObserveResult{Exists: false}, nil
	}
	api, err := a.resolveAPI(ctx, account, "")
	if err != nil {
		return ObserveResult{}, err
	}
	type describeResult struct {
		State sg.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describeResult, error) {
		observed, describeErr := api.DescribeSecurityGroup(runCtx, outputs.GroupId)
		if describeErr != nil {
			if sg.IsNotFound(describeErr) {
				return describeResult{Found: false}, nil
			}
			return describeResult{}, describeErr
		}
		return describeResult{State: observed, Found: true}, nil
	})
	if err != nil {
		return ObserveResult{}, err
	}
	if !result.Found {
		return ObserveResult{Exists: false}, nil
	}
	normalized, _ := a.NormalizeOutputs(outputs)
	return ObserveResult{Exists: true, UpToDate: !sg.HasDrift(desired, result.State), Outputs: normalized}, nil
}

func (a *SecurityGroupAdapter) DefaultTimeouts() types.ResourceTimeouts {
	return types.ResourceTimeouts{Create: "5m", Update: "5m", Delete: "5m"}
}

func (a *SecurityGroupAdapter) resolveAPI(ctx restate.Context, account, region string) (sg.SGAPI, error) {
	if a.staticAPI != nil {
		return a.staticAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("SecurityGroup adapter planning API is not configured")
	}
	config, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve SecurityGroup planning account %q: %w", account, err)
	}
	if strings.TrimSpace(region) != "" {
		config.Region = strings.TrimSpace(region)
	}
	return a.apiFactory(config), nil
}

func lookupSecurityGroup(ctx restate.RunContext, api sg.SGAPI, filter LookupFilter) (sg.ObservedState, error) {
	if strings.TrimSpace(filter.ID) != "" {
		return api.DescribeSecurityGroup(ctx, strings.TrimSpace(filter.ID))
	}
	tags := lookupTags(filter)
	if len(tags) == 0 {
		return sg.ObservedState{}, fmt.Errorf("SecurityGroup lookup requires at least one of: id, name, tag")
	}
	id, err := api.FindByTags(ctx, tags)
	if err != nil {
		return sg.ObservedState{}, err
	}
	if strings.TrimSpace(id) == "" {
		return sg.ObservedState{}, fmt.Errorf("not found")
	}
	return api.DescribeSecurityGroup(ctx, id)
}

func matchesSecurityGroupFilter(observed sg.ObservedState, filter LookupFilter) bool {
	if strings.TrimSpace(filter.ID) != "" && observed.GroupId != strings.TrimSpace(filter.ID) {
		return false
	}
	if strings.TrimSpace(filter.Name) != "" && observed.Tags["Name"] != strings.TrimSpace(filter.Name) {
		return false
	}
	for key, value := range filter.Tag {
		if observed.Tags[key] != value {
			return false
		}
	}
	return true
}

func securityGroupARN(region, ownerID, groupID string) string {
	if strings.TrimSpace(region) == "" || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(groupID) == "" {
		return ""
	}
	return fmt.Sprintf("arn:aws:ec2:%s:%s:security-group/%s", region, ownerID, groupID)
}
