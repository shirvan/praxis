package iaminstanceprofile

import (
	"fmt"
	"sort"
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
	apiFactory func(aws.Config) IAMInstanceProfileAPI
}

// NewGenericIAMInstanceProfileDriver binds IAM instance-profile semantics to
// the shared lifecycle kernel.
func NewGenericIAMInstanceProfileDriver(auth authservice.AuthClient) *kernel.Driver[IAMInstanceProfileSpec, IAMInstanceProfileOutputs, ObservedState] {
	return newGenericIAMInstanceProfileDriverWithFactory(auth, nil)
}

func newGenericIAMInstanceProfileDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) IAMInstanceProfileAPI) *kernel.Driver[IAMInstanceProfileSpec, IAMInstanceProfileOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) IAMInstanceProfileAPI {
			return NewIAMInstanceProfileAPI(awsclient.NewIAMClient(cfg))
		}
	}
	ops := &kernelOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[IAMInstanceProfileSpec, IAMInstanceProfileOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec IAMInstanceProfileSpec) (IAMInstanceProfileSpec, error) {
			if _, err := ops.apiForAccount(ctx, spec.Account); err != nil {
				return IAMInstanceProfileSpec{}, drivers.ClassifyCredentialError(err)
			}
			return applyDefaults(spec), nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) IAMInstanceProfileSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, seed IAMInstanceProfileOutputs) IAMInstanceProfileOutputs {
			outputs := outputsFromObserved(observed)
			if outputs.Arn == "" {
				outputs.Arn = seed.Arn
			}
			if outputs.InstanceProfileId == "" {
				outputs.InstanceProfileId = seed.InstanceProfileId
			}
			if outputs.InstanceProfileName == "" {
				outputs.InstanceProfileName = seed.InstanceProfileName
			}
			return outputs
		},
		HasDrift: HasDrift,
	})
}

func (o *kernelOperations) Observe(ctx restate.ObjectContext, desired IAMInstanceProfileSpec, outputs IAMInstanceProfileOutputs) (kernel.Observation[ObservedState], error) {
	name := outputs.InstanceProfileName
	if name == "" {
		name = desired.InstanceProfileName
	}
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return o.observeProfile(ctx, api, name)
}

func (o *kernelOperations) Create(ctx restate.ObjectContext, desired IAMInstanceProfileSpec) (kernel.CreateResult[IAMInstanceProfileOutputs], error) {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[IAMInstanceProfileOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	outputs, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (IAMInstanceProfileOutputs, error) {
		arn, profileID, runErr := api.CreateInstanceProfile(rc, desired)
		return IAMInstanceProfileOutputs{
			Arn: arn, InstanceProfileId: profileID, InstanceProfileName: desired.InstanceProfileName,
		}, runErr
	}, classifyMutation)
	return kernel.CreateResult[IAMInstanceProfileOutputs]{SeedOutputs: outputs}, err
}

func (o *kernelOperations) Converge(ctx restate.ObjectContext, desired IAMInstanceProfileSpec, observed ObservedState) error {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	return o.convergeProfile(ctx, api, desired, observed)
}

func (o *kernelOperations) Delete(ctx restate.ObjectContext, desired IAMInstanceProfileSpec, outputs IAMInstanceProfileOutputs) error {
	name := outputs.InstanceProfileName
	if name == "" {
		name = desired.InstanceProfileName
	}
	if name == "" {
		return nil
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}

	observation, err := o.observeProfile(ctx, api, name)
	if err != nil || !observation.Exists {
		return err
	}

	// roleName is an explicit, required component of the profile spec. The
	// profile owns exactly this one IAM association, so it is removed before
	// deleting the profile. No unrelated role, EC2, or credential resources are
	// inspected or cleaned up.
	if observation.Value.RoleName != "" {
		roleName := observation.Value.RoleName
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.RemoveRoleFromInstanceProfile(rc, name, roleName)
			if IsNotFound(runErr) {
				runErr = nil
			}
			return restate.Void{}, runErr
		}, classifyMutation); err != nil {
			return fmt.Errorf("remove owned role association %s: %w", roleName, err)
		}
	}

	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteInstanceProfile(rc, name)
		if IsNotFound(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyMutation)
	return err
}

func (o *kernelOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return o.observeProfile(ctx, api, strings.TrimSpace(ref.ResourceID))
}

func (o *kernelOperations) observeProfile(ctx restate.ObjectContext, api IAMInstanceProfileAPI, name string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, runErr := api.DescribeInstanceProfile(rc, name)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: runErr == nil, Value: observed}, runErr
	}, classifyObserve)
}

func (o *kernelOperations) convergeProfile(ctx restate.ObjectContext, api IAMInstanceProfileAPI, desired IAMInstanceProfileSpec, observed ObservedState) error {
	if desired.InstanceProfileName != observed.InstanceProfileName {
		return restate.TerminalError(fmt.Errorf("instanceProfileName is immutable; delete and recreate the instance profile to change its name"), 409)
	}
	if desired.Path != observed.Path {
		return restate.TerminalError(fmt.Errorf("path is immutable; delete and recreate the instance profile to change the path"), 409)
	}

	if desired.RoleName != observed.RoleName {
		if observed.RoleName != "" {
			oldRole := observed.RoleName
			if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
				runErr := api.RemoveRoleFromInstanceProfile(rc, desired.InstanceProfileName, oldRole)
				if IsNotFound(runErr) {
					runErr = nil
				}
				return restate.Void{}, runErr
			}, classifyMutation); err != nil {
				return fmt.Errorf("remove role association %s: %w", oldRole, err)
			}
		}
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.AddRoleToInstanceProfile(rc, desired.InstanceProfileName, desired.RoleName)
		}, classifyMutation); err != nil {
			return fmt.Errorf("add role association %s: %w", desired.RoleName, err)
		}
	}

	addTags, removeKeys := diffTags(desired.Tags, observed.Tags)
	if len(addTags) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.TagInstanceProfile(rc, desired.InstanceProfileName, addTags)
		}, classifyMutation); err != nil {
			return fmt.Errorf("tag instance profile: %w", err)
		}
	}
	if len(removeKeys) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UntagInstanceProfile(rc, desired.InstanceProfileName, removeKeys)
		}, classifyMutation); err != nil {
			return fmt.Errorf("untag instance profile: %w", err)
		}
	}
	return nil
}

func (o *kernelOperations) apiForAccount(ctx restate.ObjectContext, account string) (IAMInstanceProfileAPI, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, fmt.Errorf("IAMInstanceProfile driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAM account %q: %w", account, err)
	}
	return o.apiFactory(cfg), nil
}

func classifyObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyMutation(err error) error {
	if err == nil {
		return nil
	}
	if awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if IsNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	if IsAlreadyExists(err) || IsDeleteConflict(err) {
		return restate.TerminalError(err, 409)
	}
	if IsLimitExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	return err
}

func validateSpec(spec IAMInstanceProfileSpec) error {
	if spec.InstanceProfileName == "" {
		return fmt.Errorf("instanceProfileName is required")
	}
	if spec.RoleName == "" {
		return fmt.Errorf("roleName is required")
	}
	return nil
}

func applyDefaults(spec IAMInstanceProfileSpec) IAMInstanceProfileSpec {
	spec.Account = strings.TrimSpace(spec.Account)
	spec.Path = strings.TrimSpace(spec.Path)
	spec.InstanceProfileName = strings.TrimSpace(spec.InstanceProfileName)
	spec.RoleName = strings.TrimSpace(spec.RoleName)
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func specFromObserved(observed ObservedState) IAMInstanceProfileSpec {
	return IAMInstanceProfileSpec{
		Path: observed.Path, InstanceProfileName: observed.InstanceProfileName,
		RoleName: observed.RoleName, Tags: drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) IAMInstanceProfileOutputs {
	return IAMInstanceProfileOutputs{
		Arn: observed.Arn, InstanceProfileId: observed.InstanceProfileId,
		InstanceProfileName: observed.InstanceProfileName,
	}
}

func diffTags(desired, observed map[string]string) (map[string]string, []string) {
	filteredDesired := drivers.FilterPraxisTags(desired)
	filteredObserved := drivers.FilterPraxisTags(observed)
	add := make(map[string]string)
	for key, value := range filteredDesired {
		if observedValue, ok := filteredObserved[key]; !ok || observedValue != value {
			add[key] = value
		}
	}
	remove := make([]string, 0)
	for key := range filteredObserved {
		if _, ok := filteredDesired[key]; !ok {
			remove = append(remove, key)
		}
	}
	sort.Strings(remove)
	return add, remove
}
