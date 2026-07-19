package ami

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

type genericOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) AMIAPI
}

func NewGenericAMIDriver(auth authservice.AuthClient) *kernel.Driver[AMISpec, AMIOutputs, ObservedState] {
	return newGenericAMIDriverWithFactory(auth, nil)
}
func newGenericAMIDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) AMIAPI) *kernel.Driver[AMISpec, AMIOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) AMIAPI { return NewAMIAPI(awsclient.NewEC2Client(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[AMISpec, AMIOutputs, ObservedState]{ServiceName: ServiceName, Capabilities: kernel.Capabilities{Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true, Readiness: true, ConvergeWhilePending: true}, Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec AMISpec) (AMISpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return AMISpec{}, drivers.ClassifyCredentialError(err)
			}
			if spec.Region == "" {
				spec.Region = region
			}
			if spec.Name == "" {
				spec.Name = strings.TrimSpace(spec.Tags["Name"])
			}
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate: func(spec AMISpec) error {
			if spec.Region == "" {
				return fmt.Errorf("region is required")
			}
			if spec.Name == "" {
				return fmt.Errorf("name is required")
			}
			return validateSource(spec.Source)
		},
		ValidateImport: func(spec AMISpec) error {
			if spec.Region == "" || spec.Name == "" {
				return fmt.Errorf("region and name are required")
			}
			return nil
		},
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) AMISpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ AMIOutputs) AMIOutputs { return outputsFromObserved(observed) }, FieldDiffs: ComputeFieldDiffs,
		HasDrift: func(desired AMISpec, observed ObservedState) bool {
			desired.Tags = desiredTags(desired)
			return HasDrift(desired, observed)
		},
		CheckReadiness: func(observed ObservedState) kernel.ReadinessResult {
			switch observed.State {
			case "available":
				return kernel.ReadinessResult{Phase: kernel.ReadinessReady}
			case "pending", "":
				return kernel.ReadinessResult{Phase: kernel.ReadinessPending, Message: "AMI is still becoming available"}
			default:
				return kernel.ReadinessResult{Phase: kernel.ReadinessFailed, Message: "AMI entered state " + observed.State}
			}
		},
	})
}
func (o *genericOperations) Observe(ctx restate.ObjectContext, desired AMISpec, outputs AMIOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	if outputs.ImageId != "" {
		return observeAMI(ctx, api, outputs.ImageId)
	}
	if desired.ManagedKey == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	id, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) { return api.FindByManagedKey(rc, desired.ManagedKey) }, classifyAMIObserve)
	if err != nil || id == "" {
		return kernel.Observation[ObservedState]{}, err
	}
	return observeAMI(ctx, api, id)
}
func (o *genericOperations) Create(ctx restate.ObjectContext, desired AMISpec) (kernel.CreateResult[AMIOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[AMIOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	id, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		if found, e := api.FindByManagedKey(rc, desired.ManagedKey); e != nil || found != "" {
			return found, e
		}
		if desired.Source.FromSnapshot != nil {
			return api.RegisterImage(rc, desired)
		}
		return api.CopyImage(rc, desired)
	}, classifyAMIMutation)
	return kernel.CreateResult[AMIOutputs]{SeedOutputs: AMIOutputs{ImageId: id, Name: desired.Name}}, err
}
func (o *genericOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next AMISpec, _ ObservedState) error {
	if previous.Name != "" && previous.Name != next.Name || !reflect.DeepEqual(previous.Source, next.Source) {
		return restate.TerminalError(fmt.Errorf("AMI name and source are immutable; delete and reprovision the AMI"), 409)
	}
	return nil
}
func (o *genericOperations) Converge(ctx restate.ObjectContext, desired AMISpec, observed ObservedState) error {
	if desired.Name != observed.Name {
		return restate.TerminalError(fmt.Errorf("AMI name is immutable; delete and reprovision the AMI"), 409)
	}
	if owner := observed.Tags["praxis:managed-key"]; owner != "" && owner != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf("AMI is owned by Praxis object %q", owner), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	run := func(fn func(restate.RunContext) error) error {
		_, e := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) { return restate.Void{}, fn(rc) }, classifyAMIMutation)
		return e
	}
	if desired.Description != observed.Description {
		if err := run(func(rc restate.RunContext) error {
			return api.ModifyDescription(rc, observed.ImageId, desired.Description)
		}); err != nil {
			return err
		}
	}
	if !drivers.TagsMatch(desiredTags(desired), observed.Tags) || observed.Tags["praxis:managed-key"] != desired.ManagedKey {
		if err := run(func(rc restate.RunContext) error { return api.UpdateTags(rc, observed.ImageId, desiredTags(desired)) }); err != nil {
			return err
		}
	}
	if hasLaunchPermDrift(desired.LaunchPermissions, observed) {
		if err := run(func(rc restate.RunContext) error {
			return api.ModifyLaunchPermissions(rc, observed.ImageId, desired.LaunchPermissions)
		}); err != nil {
			return err
		}
	}
	if hasDeprecationDrift(desired.Deprecation, observed.DeprecationTime) {
		if desired.Deprecation == nil {
			return run(func(rc restate.RunContext) error { return api.DisableDeprecation(rc, observed.ImageId) })
		}
		return run(func(rc restate.RunContext) error {
			return api.EnableDeprecation(rc, observed.ImageId, desired.Deprecation.DeprecateAt)
		})
	}
	return nil
}
func (o *genericOperations) Delete(ctx restate.ObjectContext, desired AMISpec, outputs AMIOutputs) error {
	if outputs.ImageId == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	observed, err := observeAMI(ctx, api, outputs.ImageId)
	if err != nil || !observed.Exists {
		return err
	}
	if owner := observed.Value.Tags["praxis:managed-key"]; owner != "" && owner != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf("refusing to delete AMI owned by %q", owner), 409)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		e := api.DeregisterImage(rc, outputs.ImageId)
		if IsNotFound(e) {
			e = nil
		}
		return restate.Void{}, e
	}, classifyAMIMutation)
	return err
}
func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		var observed ObservedState
		var e error
		if looksLikeAMIID(ref.ResourceID) {
			observed, e = api.DescribeImage(rc, ref.ResourceID)
		} else {
			observed, e = api.DescribeImageByName(rc, ref.ResourceID)
		}
		if IsNotFound(e) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if e != nil {
			return kernel.Observation[ObservedState]{}, e
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyAMIObserve)
}
func observeAMI(ctx restate.ObjectContext, api AMIAPI, id string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeImage(rc, id)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyAMIObserve)
}
func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (AMIAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("AMI driver is not configured")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve AMI account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}
func classifyAMIObserve(err error) error {
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
func classifyAMIMutation(err error) error {
	if err == nil || restate.IsTerminalError(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsSnapshotNotFound(err) || IsInvalidParam(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if IsAMIQuotaExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	return err
}
