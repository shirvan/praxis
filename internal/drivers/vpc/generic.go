package vpc

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/drivers/kernel"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type genericOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) VPCAPI
}

// NewGenericVPCDriver binds VPC behavior to the shared generic lifecycle kernel.
func NewGenericVPCDriver(auth authservice.AuthClient) *kernel.Driver[VPCSpec, VPCOutputs, ObservedState] {
	return newGenericVPCDriverWithFactory(auth, nil)
}

func newGenericVPCDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) VPCAPI) *kernel.Driver[VPCSpec, VPCOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) VPCAPI { return NewVPCAPI(awsclient.NewEC2Client(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[VPCSpec, VPCOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
			ManagedDriftCorrection: true, LateInitialization: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec VPCSpec) (VPCSpec, error) {
			if _, _, err := ops.apiForAccount(ctx, spec.Account); err != nil {
				return VPCSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateProvisionSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) VPCSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			spec.Region = observed.Region
			return spec
		},
		OutputsFromObserved: outputsFromObserved,
		HasDrift:            HasDrift,
		LateInitialize:      LateInitVPC,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired VPCSpec, outputs VPCOutputs) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	vpcID := strings.TrimSpace(outputs.VpcId)
	if vpcID == "" && desired.ManagedKey != "" {
		vpcID, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, desired.ManagedKey)
		}, classifyVPCFind)
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		if vpcID != "" {
			return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
				"VPC name %q in this region is already managed by Praxis (vpcId: %s); remove the existing resource or use a different metadata.name",
				desired.ManagedKey, vpcID,
			), 409)
		}
	}
	if vpcID == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return observeVPC(ctx, api, region, vpcID)
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired VPCSpec) (kernel.CreateResult[VPCOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[VPCOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	vpcID, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		// CreateVpc has no idempotency token. Recheck the ownership marker in the
		// durable callback so a retry after an ambiguous create response recovers
		// the VPC instead of creating a duplicate.
		if desired.ManagedKey != "" {
			existing, findErr := api.FindByManagedKey(rc, desired.ManagedKey)
			if findErr != nil || existing != "" {
				return existing, findErr
			}
		}
		return api.CreateVpc(rc, desired)
	}, classifyVPCCreate)
	if err != nil {
		return kernel.CreateResult[VPCOutputs]{}, err
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.WaitUntilAvailable(rc, vpcID)
	}, classifyVPCMutation)
	return kernel.CreateResult[VPCOutputs]{SeedOutputs: VPCOutputs{VpcId: vpcID}}, err
}

func (o *genericOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next VPCSpec, _ ObservedState) error {
	switch {
	case previous.Account != next.Account:
		return restate.TerminalError(fmt.Errorf("account is immutable; delete and reprovision to change it"), 409)
	case previous.Region != next.Region:
		return restate.TerminalError(fmt.Errorf("region is immutable; delete and reprovision to change it"), 409)
	case previous.CidrBlock != next.CidrBlock:
		return restate.TerminalError(fmt.Errorf("cidrBlock is immutable; delete and reprovision to change it"), 409)
	case previous.InstanceTenancy != next.InstanceTenancy:
		return restate.TerminalError(fmt.Errorf("instanceTenancy is immutable; delete and reprovision to change it"), 409)
	default:
		return nil
	}
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired VPCSpec, observed ObservedState) error {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	vpcID := observed.VpcId
	if vpcID == "" {
		return restate.TerminalError(fmt.Errorf("cannot converge VPC without a vpcId"), 500)
	}

	// AWS requires support before hostnames when enabling and hostnames before
	// support when disabling.
	if desired.EnableDnsSupport && !observed.EnableDnsSupport {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyDnsSupport(rc, vpcID, true)
		}, classifyVPCMutation); err != nil {
			return fmt.Errorf("modify DNS support: %w", err)
		}
	}
	if desired.EnableDnsHostnames != observed.EnableDnsHostnames {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyDnsHostnames(rc, vpcID, desired.EnableDnsHostnames)
		}, classifyVPCMutation); err != nil {
			return fmt.Errorf("modify DNS hostnames: %w", err)
		}
	}
	if !desired.EnableDnsSupport && observed.EnableDnsSupport {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyDnsSupport(rc, vpcID, false)
		}, classifyVPCMutation); err != nil {
			return fmt.Errorf("modify DNS support: %w", err)
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, vpcID, desired.Tags)
		}, classifyVPCMutation); err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}
	return nil
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired VPCSpec, outputs VPCOutputs) error {
	if outputs.IsDefault {
		return restate.TerminalError(fmt.Errorf(
			"cannot delete VPC %s: it is the default VPC for this region; default VPC deletion must be done manually via the AWS console",
			outputs.VpcId,
		), 409)
	}
	if outputs.VpcId == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteVpc(rc, outputs.VpcId)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyVPCMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeVPC(ctx, api, region, strings.TrimSpace(ref.ResourceID))
}

func observeVPC(ctx restate.ObjectContext, api VPCAPI, region, vpcID string) (kernel.Observation[ObservedState], error) {
	observation, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, describeErr := api.DescribeVpc(rc, vpcID)
		if IsNotFound(describeErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if describeErr != nil {
			return kernel.Observation[ObservedState]{}, describeErr
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyVPCObserve)
	if observation.Exists {
		observation.Value.Region = region
	}
	return observation, err
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (VPCAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("VPC driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve VPC account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func validateProvisionSpec(spec VPCSpec) error {
	if spec.CidrBlock == "" {
		return fmt.Errorf("cidrBlock is required")
	}
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.EnableDnsHostnames && !spec.EnableDnsSupport {
		return fmt.Errorf("enableDnsHostnames requires enableDnsSupport to be true")
	}
	return nil
}

func outputsFromObserved(observed ObservedState, seed VPCOutputs) VPCOutputs {
	vpcID := observed.VpcId
	if vpcID == "" {
		vpcID = seed.VpcId
	}
	return VPCOutputs{
		VpcId: vpcID, CidrBlock: observed.CidrBlock, State: observed.State,
		EnableDnsHostnames: observed.EnableDnsHostnames, EnableDnsSupport: observed.EnableDnsSupport,
		InstanceTenancy: observed.InstanceTenancy, OwnerId: observed.OwnerId,
		DhcpOptionsId: observed.DhcpOptionsId, IsDefault: observed.IsDefault,
		ARN: fmt.Sprintf("arn:aws:ec2:%s:%s:vpc/%s", observed.Region, observed.OwnerId, vpcID),
	}
}

func specFromObserved(observed ObservedState) VPCSpec {
	return VPCSpec{
		CidrBlock: observed.CidrBlock, EnableDnsHostnames: observed.EnableDnsHostnames,
		EnableDnsSupport: observed.EnableDnsSupport, InstanceTenancy: observed.InstanceTenancy,
		Tags: observed.Tags,
	}
}

func classifyVPCObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParam(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyVPCFind(err error) error {
	if err != nil && strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 500)
	}
	return classifyVPCObserve(err)
}

func classifyVPCCreate(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 500)
	}
	if IsCidrConflict(err) {
		return restate.TerminalError(err, 409)
	}
	return classifyVPCMutation(err)
}

func classifyVPCMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	if IsInvalidParam(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	// DependencyViolation and provider availability failures are retryable.
	return err
}
