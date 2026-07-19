package ec2

import (
	"fmt"
	"reflect"
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
	apiFactory func(aws.Config) EC2API
}

// NewGenericEC2InstanceDriver binds EC2 instance behavior to the shared
// lifecycle kernel. EC2-specific identity recovery, waiters, convergence, and
// error classification remain in this package.
func NewGenericEC2InstanceDriver(auth authservice.AuthClient) *kernel.Driver[EC2InstanceSpec, EC2InstanceOutputs, ObservedState] {
	return NewGenericEC2InstanceDriverWithFactory(auth, nil)
}

func NewGenericEC2InstanceDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) EC2API) *kernel.Driver[EC2InstanceSpec, EC2InstanceOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) EC2API { return NewEC2API(awsclient.NewEC2Client(cfg)) }
	}
	ops := &kernelOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[EC2InstanceSpec, EC2InstanceOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
			ManagedDriftCorrection: true, LateInitialization: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec EC2InstanceSpec) (EC2InstanceSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return EC2InstanceSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec.Region = strings.TrimSpace(spec.Region)
			if spec.Region == "" {
				spec.Region = region
			}
			spec.ImageId = strings.TrimSpace(spec.ImageId)
			spec.InstanceType = strings.TrimSpace(spec.InstanceType)
			spec.SubnetId = strings.TrimSpace(spec.SubnetId)
			spec.IamInstanceProfile = strings.TrimSpace(spec.IamInstanceProfile)
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) EC2InstanceSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: outputsFromObserved,
		FieldDiffs:          ComputeFieldDiffs,
		HasDrift:            HasDrift,
		LateInitialize:      LateInitEC2Instance,
	})
}

func (o *kernelOperations) Observe(ctx restate.ObjectContext, desired EC2InstanceSpec, outputs EC2InstanceOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}

	instanceID := outputs.InstanceId
	if instanceID == "" && desired.ManagedKey != "" {
		instanceID, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, desired.ManagedKey)
		}, classifyFind)
		if err != nil || instanceID == "" {
			return kernel.Observation[ObservedState]{}, err
		}
	}
	if instanceID == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return observeInstance(ctx, api, instanceID)
}

func (o *kernelOperations) Create(ctx restate.ObjectContext, desired EC2InstanceSpec) (kernel.CreateResult[EC2InstanceOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[EC2InstanceOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	clientToken := runInstancesClientToken(desired.ManagedKey, ctx.Request().ID)
	instanceID, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		return api.RunInstance(rc, desired, clientToken)
	}, classifyMutation)
	if err != nil {
		return kernel.CreateResult[EC2InstanceOutputs]{}, err
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.WaitUntilRunning(rc, instanceID)
	}, classifyWait)
	return kernel.CreateResult[EC2InstanceOutputs]{
		SeedOutputs: EC2InstanceOutputs{InstanceId: instanceID},
	}, err
}

func (o *kernelOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next EC2InstanceSpec, _ ObservedState) error {
	switch {
	case previous.Account != next.Account:
		return restate.TerminalError(fmt.Errorf("account is immutable; delete and reprovision to change it"), 409)
	case previous.Region != next.Region:
		return restate.TerminalError(fmt.Errorf("region is immutable; delete and reprovision to change it"), 409)
	case previous.ImageId != next.ImageId:
		return restate.TerminalError(fmt.Errorf("imageId is immutable; delete and reprovision to change it"), 409)
	case previous.SubnetId != next.SubnetId:
		return restate.TerminalError(fmt.Errorf("subnetId is immutable; delete and reprovision to change it"), 409)
	case previous.KeyName != next.KeyName:
		return restate.TerminalError(fmt.Errorf("keyName is immutable; delete and reprovision to change it"), 409)
	case previous.UserData != next.UserData:
		return restate.TerminalError(fmt.Errorf("userData is immutable; delete and reprovision to change it"), 409)
	case !reflect.DeepEqual(previous.RootVolume, next.RootVolume):
		return restate.TerminalError(fmt.Errorf("rootVolume is immutable; delete and reprovision to change it"), 409)
	default:
		return nil
	}
}

func (o *kernelOperations) Converge(ctx restate.ObjectContext, desired EC2InstanceSpec, observed ObservedState) error {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	instanceID := observed.InstanceId
	if instanceID == "" {
		return restate.TerminalError(fmt.Errorf("cannot converge EC2 instance without an instanceId"), 500)
	}

	if desired.InstanceType != observed.InstanceType {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyInstanceType(rc, instanceID, desired.InstanceType)
		}, classifyMutation); err != nil {
			return fmt.Errorf("modify instance type: %w", err)
		}
	}
	if len(desired.SecurityGroupIds) > 0 && !securityGroupsMatch(desired.SecurityGroupIds, observed.SecurityGroupIds) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifySecurityGroups(rc, instanceID, desired.SecurityGroupIds)
		}, classifyMutation); err != nil {
			return fmt.Errorf("modify security groups: %w", err)
		}
	}
	if !instanceProfilesMatch(desired.IamInstanceProfile, observed.IamInstanceProfile) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateIAMInstanceProfile(rc, instanceID, desired.IamInstanceProfile)
		}, classifyMutation); err != nil {
			return fmt.Errorf("update IAM instance profile: %w", err)
		}
	}
	if desired.Monitoring != observed.Monitoring {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateMonitoring(rc, instanceID, desired.Monitoring)
		}, classifyMutation); err != nil {
			return fmt.Errorf("update monitoring: %w", err)
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, instanceID, desired.Tags)
		}, classifyMutation); err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}
	return nil
}

func (o *kernelOperations) Delete(ctx restate.ObjectContext, desired EC2InstanceSpec, outputs EC2InstanceOutputs) error {
	if outputs.InstanceId == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.TerminateInstance(rc, outputs.InstanceId)
		if IsNotFound(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyMutation)
	return err
}

func (o *kernelOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	result, err := observeInstance(ctx, api, strings.TrimSpace(ref.ResourceID))
	if err != nil {
		return result, err
	}
	if isTerminating(result.Value.State) {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(
			fmt.Errorf("import failed: instance %s is %s", ref.ResourceID, result.Value.State), 400)
	}
	if !result.Exists {
		return result, nil
	}
	return result, nil
}

func observeInstance(ctx restate.ObjectContext, api EC2API, instanceID string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeInstance(rc, instanceID)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		if isTerminating(observed.State) {
			return kernel.Observation[ObservedState]{Value: observed}, nil
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyObserve)
}

func (o *kernelOperations) apiForAccount(ctx restate.ObjectContext, account string) (EC2API, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("EC2Instance driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve EC2 account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func validateSpec(spec EC2InstanceSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.ImageId == "" {
		return fmt.Errorf("imageId is required")
	}
	if spec.InstanceType == "" {
		return fmt.Errorf("instanceType is required")
	}
	if spec.SubnetId == "" {
		return fmt.Errorf("subnetId is required")
	}
	return nil
}

func outputsFromObserved(observed ObservedState, seed EC2InstanceOutputs) EC2InstanceOutputs {
	instanceID := observed.InstanceId
	if instanceID == "" {
		instanceID = seed.InstanceId
	}
	return EC2InstanceOutputs{
		InstanceId: instanceID, PrivateIpAddress: observed.PrivateIpAddress,
		PublicIpAddress: observed.PublicIpAddress, PrivateDnsName: observed.PrivateDnsName,
		State: observed.State, SubnetId: observed.SubnetId, VpcId: observed.VpcId,
	}
}

func specFromObserved(obs ObservedState) EC2InstanceSpec {
	return EC2InstanceSpec{
		ImageId: obs.ImageId, InstanceType: obs.InstanceType, KeyName: obs.KeyName,
		SubnetId: obs.SubnetId, SecurityGroupIds: obs.SecurityGroupIds,
		IamInstanceProfile: obs.IamInstanceProfile, Monitoring: obs.Monitoring,
		Tags: drivers.FilterPraxisTags(obs.Tags),
	}
}

func isTerminating(state string) bool {
	return state == "terminated" || state == "shutting-down"
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
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 500)
	}
	return classifyObserve(err)
}

func classifyMutation(err error) error {
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
	if IsInsufficientCapacity(err) {
		return restate.TerminalError(err, 503)
	}
	return err
}

func classifyWait(err error) error {
	return classifyMutation(err)
}
