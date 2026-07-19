package subnet

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

type kernelOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) SubnetAPI
}

// NewGenericSubnetDriver binds EC2 subnet semantics to the shared lifecycle
// kernel. Subnet creation retains the EC2 availability waiter, while mutable
// public-IP and tag drift is converged in place.
func NewGenericSubnetDriver(auth authservice.AuthClient) *kernel.Driver[SubnetSpec, SubnetOutputs, ObservedState] {
	return newGenericSubnetDriverWithFactory(auth, nil)
}

func newGenericSubnetDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) SubnetAPI) *kernel.Driver[SubnetSpec, SubnetOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) SubnetAPI { return NewSubnetAPI(awsclient.NewEC2Client(cfg)) }
	}
	ops := &kernelOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[SubnetSpec, SubnetOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec SubnetSpec) (SubnetSpec, error) {
			spec = applyDefaults(spec)
			_, _, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return SubnetSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) SubnetSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			spec.Region = observed.Region
			return spec
		},
		OutputsFromObserved: outputsFromObserved,
		FieldDiffs:          ComputeFieldDiffs,
		HasDrift:            HasDrift,
	})
}

func (o *kernelOperations) Observe(ctx restate.ObjectContext, desired SubnetSpec, outputs SubnetOutputs) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	subnetID := strings.TrimSpace(outputs.SubnetId)
	if subnetID == "" && desired.ManagedKey != "" {
		subnetID, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, desired.ManagedKey)
		}, classifyFind)
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		if subnetID != "" {
			return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf(
				"subnet name %q in VPC %s is already managed by Praxis (subnetId: %s); remove the existing resource or use a different metadata.name",
				desired.ManagedKey, desired.VpcId, subnetID,
			), 409)
		}
	}
	if subnetID == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return o.observeSubnet(ctx, api, region, subnetID)
}

func (o *kernelOperations) Create(ctx restate.ObjectContext, desired SubnetSpec) (kernel.CreateResult[SubnetOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[SubnetOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	subnetID, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		// CreateSubnet has no idempotency token. Recheck the ownership marker in
		// the durable callback so an ambiguous response cannot create a duplicate.
		if desired.ManagedKey != "" {
			existing, findErr := api.FindByManagedKey(rc, desired.ManagedKey)
			if findErr != nil || existing != "" {
				return existing, findErr
			}
		}
		return api.CreateSubnet(rc, desired)
	}, classifyCreate)
	if err != nil {
		return kernel.CreateResult[SubnetOutputs]{}, err
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.WaitUntilAvailable(rc, subnetID)
	}, classifyMutation)
	return kernel.CreateResult[SubnetOutputs]{SeedOutputs: SubnetOutputs{SubnetId: subnetID}}, err
}

func (o *kernelOperations) Converge(ctx restate.ObjectContext, desired SubnetSpec, observed ObservedState, currentOutputs SubnetOutputs) (SubnetOutputs, error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return currentOutputs, drivers.ClassifyCredentialError(err)
	}
	if observed.SubnetId == "" {
		return currentOutputs, restate.TerminalError(fmt.Errorf("cannot converge subnet without a subnetId"), 500)
	}
	if desired.MapPublicIpOnLaunch != observed.MapPublicIpOnLaunch {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyMapPublicIp(rc, observed.SubnetId, desired.MapPublicIpOnLaunch)
		}, classifyMutation); err != nil {
			return currentOutputs, fmt.Errorf("modify mapPublicIpOnLaunch: %w", err)
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.SubnetId, desired.Tags)
		}, classifyMutation); err != nil {
			return currentOutputs, fmt.Errorf("update tags: %w", err)
		}
	}
	return currentOutputs, nil
}

func (o *kernelOperations) Delete(ctx restate.ObjectContext, desired SubnetSpec, outputs SubnetOutputs) error {
	if outputs.SubnetId == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteSubnet(rc, outputs.SubnetId)
		if IsNotFound(runErr) {
			runErr = nil
		}
		if IsDependencyViolation(runErr) {
			runErr = restate.TerminalError(fmt.Errorf(
				"cannot delete subnet %s: dependent resources exist in the subnet; remove instances, network interfaces, NAT gateways, or other attached resources first",
				outputs.SubnetId,
			), 409)
		}
		return restate.Void{}, runErr
	}, classifyMutation)
	return err
}

func (o *kernelOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return o.observeSubnet(ctx, api, region, strings.TrimSpace(ref.ResourceID))
}

func (o *kernelOperations) observeSubnet(ctx restate.ObjectContext, api SubnetAPI, region, subnetID string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, runErr := api.DescribeSubnet(rc, subnetID)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		observed.Region = region
		return kernel.Observation[ObservedState]{Exists: runErr == nil, Value: observed}, runErr
	}, classifyObserve)
}

func (o *kernelOperations) apiForAccount(ctx restate.ObjectContext, account string) (SubnetAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("subnet driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve Subnet account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func applyDefaults(spec SubnetSpec) SubnetSpec {
	spec.Account = strings.TrimSpace(spec.Account)
	spec.Region = strings.TrimSpace(spec.Region)
	spec.VpcId = strings.TrimSpace(spec.VpcId)
	spec.CidrBlock = strings.TrimSpace(spec.CidrBlock)
	spec.AvailabilityZone = strings.TrimSpace(spec.AvailabilityZone)
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec SubnetSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.VpcId == "" {
		return fmt.Errorf("vpcId is required")
	}
	if spec.CidrBlock == "" {
		return fmt.Errorf("cidrBlock is required")
	}
	if spec.AvailabilityZone == "" {
		return fmt.Errorf("availabilityZone is required")
	}
	return nil
}

func specFromObserved(observed ObservedState) SubnetSpec {
	return SubnetSpec{
		Region: observed.Region, VpcId: observed.VpcId, CidrBlock: observed.CidrBlock,
		AvailabilityZone: observed.AvailabilityZone, MapPublicIpOnLaunch: observed.MapPublicIpOnLaunch,
		Tags: drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState, seed SubnetOutputs) SubnetOutputs {
	subnetID := observed.SubnetId
	if subnetID == "" {
		subnetID = seed.SubnetId
	}
	return SubnetOutputs{
		SubnetId: subnetID,
		ARN:      fmt.Sprintf("arn:aws:ec2:%s:%s:subnet/%s", observed.Region, observed.OwnerId, subnetID),
		VpcId:    observed.VpcId, CidrBlock: observed.CidrBlock,
		AvailabilityZone: observed.AvailabilityZone, AvailabilityZoneId: observed.AvailabilityZoneId,
		MapPublicIpOnLaunch: observed.MapPublicIpOnLaunch, State: observed.State,
		OwnerId: observed.OwnerId, AvailableIpCount: observed.AvailableIpCount,
	}
}

func classifyObserve(err error) error {
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

func classifyFind(err error) error {
	if err != nil && strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 500)
	}
	return classifyObserve(err)
}

func classifyCreate(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 500)
	}
	if IsCidrConflict(err) {
		return restate.TerminalError(err, 409)
	}
	return classifyMutation(err)
}

func classifyMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	if IsDependencyViolation(err) {
		return restate.TerminalError(err, 409)
	}
	if IsInvalidParam(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	return err
}
