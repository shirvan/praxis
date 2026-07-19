// EC2Instance provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + instance name.
// EC2 instances are region-scoped; the key combines the AWS region with the
// metadata.name, which is also written to the instance's Name tag.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/ec2"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// EC2Adapter is the descriptor-driven adapter for EC2Instance, extended with
// per-kind default timeouts and a post-provision readiness check.
type EC2Adapter struct {
	*GenericAdapter[ec2.EC2InstanceSpec, ec2.EC2InstanceOutputs, ec2.ObservedState]
}

func ec2Descriptor() GenericDescriptor[ec2.EC2InstanceSpec, ec2.EC2InstanceOutputs, ec2.ObservedState] {
	return GenericDescriptor[ec2.EC2InstanceSpec, ec2.EC2InstanceOutputs, ec2.ObservedState]{
		Kind:  ec2.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (ec2.EC2InstanceSpec, error) {
			var parsed ec2.EC2InstanceSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return ec2.EC2InstanceSpec{}, fmt.Errorf("decode EC2Instance spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return ec2.EC2InstanceSpec{}, fmt.Errorf("EC2Instance metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return ec2.EC2InstanceSpec{}, fmt.Errorf("EC2Instance spec.region is required")
			}
			if strings.TrimSpace(parsed.ImageId) == "" {
				return ec2.EC2InstanceSpec{}, fmt.Errorf("EC2Instance spec.imageId is required")
			}
			if strings.TrimSpace(parsed.InstanceType) == "" {
				return ec2.EC2InstanceSpec{}, fmt.Errorf("EC2Instance spec.instanceType is required")
			}
			if strings.TrimSpace(parsed.SubnetId) == "" {
				return ec2.EC2InstanceSpec{}, fmt.Errorf("EC2Instance spec.subnetId is required")
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

		KeyFromSpec: func(spec ec2.EC2InstanceSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("instance name", name); err != nil {
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

		PrepareSpec: func(spec ec2.EC2InstanceSpec, key, account string) ec2.EC2InstanceSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out ec2.EC2InstanceOutputs) map[string]any {
			result := map[string]any{
				"instanceId":       out.InstanceId,
				"privateIpAddress": out.PrivateIpAddress,
				"privateDnsName":   out.PrivateDnsName,
				"state":            out.State,
				"subnetId":         out.SubnetId,
				"vpcId":            out.VpcId,
			}
			if out.ARN != "" {
				result["arn"] = out.ARN
			}
			if out.PublicIpAddress != "" {
				result["publicIpAddress"] = out.PublicIpAddress
			}
			return result
		},

		PlanIdentity: storedPlanIdentity[ec2.EC2InstanceSpec](func(out ec2.EC2InstanceOutputs) string { return out.InstanceId }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[ec2.EC2InstanceSpec, ec2.EC2InstanceOutputs, ec2.ObservedState] {
			return ec2Probe(ec2.NewEC2API(awsclient.NewEC2Client(cfg)))
		},

		DiffFields: func(desired ec2.EC2InstanceSpec, observed ec2.ObservedState, _ ec2.EC2InstanceOutputs) []types.FieldDiff {
			rawDiffs := ec2.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// ec2Probe adapts the driver API to the generic plan probe shape. Instances in
// terminated or shutting-down states are reported as absent so the plan
// recreates them.
func ec2Probe(api ec2.EC2API) PlanProbeFunc[ec2.EC2InstanceSpec, ec2.EC2InstanceOutputs, ec2.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[ec2.EC2InstanceSpec, ec2.EC2InstanceOutputs]) (ec2.ObservedState, bool, error) {
		instanceID := input.Identity
		obs, err := api.DescribeInstance(runCtx, instanceID)
		if err != nil {
			if ec2.IsNotFound(err) {
				return ec2.ObservedState{}, false, nil
			}
			return ec2.ObservedState{}, false, err
		}
		if obs.State == "terminated" || obs.State == "shutting-down" {
			return ec2.ObservedState{}, false, nil
		}
		return obs, true, nil
	}
}

// NewEC2AdapterWithAuth builds the production adapter; plan-time credentials
// are resolved through the Auth Service.
func NewEC2AdapterWithAuth(auth authservice.AuthClient) *EC2Adapter {
	return &EC2Adapter{GenericAdapter: NewGenericAdapter(ec2Descriptor(), auth)}
}

// NewEC2AdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewEC2AdapterWithAPI(api ec2.EC2API) *EC2Adapter {
	return &EC2Adapter{GenericAdapter: NewGenericAdapterWithProbe(ec2Descriptor(), ec2Probe(api))}
}

// DefaultTimeouts provides per-kind default timeouts for EC2 instances.
func (a *EC2Adapter) DefaultTimeouts() types.ResourceTimeouts {
	return types.ResourceTimeouts{Create: "10m", Update: "10m", Delete: "10m"}
}

// WaitReady checks whether the EC2 instance has reached the Running state.
func (a *EC2Adapter) WaitReady(ctx restate.Context, key string) (WaitReadyResult, error) {
	status, err := restate.Object[types.StatusResponse](ctx, a.ServiceName(), key, "GetStatus").Request(restate.Void{})
	if err != nil {
		return WaitReadyResult{}, err
	}
	if status.Status == types.StatusReady {
		outputs, _ := fetchJSONMap(ctx, a.ServiceName(), key, "GetOutputs")
		return WaitReadyResult{Ready: true, Message: "instance running", Outputs: outputs}, nil
	}
	return WaitReadyResult{Ready: false, Message: fmt.Sprintf("instance status: %s", status.Status)}, nil
}
