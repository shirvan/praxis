package esm

import (
	"fmt"

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
	apiFactory func(aws.Config) ESMAPI
}

func NewGenericEventSourceMappingDriver(auth authservice.AuthClient) *kernel.Driver[EventSourceMappingSpec, EventSourceMappingOutputs, ObservedState] {
	return newGenericEventSourceMappingDriverWithFactory(auth, nil)
}
func newGenericEventSourceMappingDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) ESMAPI) *kernel.Driver[EventSourceMappingSpec, EventSourceMappingOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) ESMAPI { return NewESMAPI(awsclient.NewLambdaClient(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[EventSourceMappingSpec, EventSourceMappingOutputs, ObservedState]{
		ServiceName:  ServiceName,
		Capabilities: kernel.Capabilities{Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true, Readiness: true},
		Operations:   ops,
		Prepare: func(ctx restate.ObjectContext, spec EventSourceMappingSpec) (EventSourceMappingSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return EventSourceMappingSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			if spec.Region == "" {
				spec.Region = region
			}
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateProvisionSpec,
		ValidateImport: func(spec EventSourceMappingSpec) error {
			if spec.Region == "" || spec.FunctionName == "" || spec.EventSourceArn == "" {
				return fmt.Errorf("region, functionName, and eventSourceArn are required")
			}
			return nil
		},
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) EventSourceMappingSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ EventSourceMappingOutputs) EventSourceMappingOutputs {
			return outputsFromObserved(observed)
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
		CheckReadiness: func(observed ObservedState) kernel.ReadinessResult {
			switch observed.State {
			case "Enabled", "Disabled", "Deleted", "":
				return kernel.ReadinessResult{Phase: kernel.ReadinessReady}
			case "Creating", "Enabling", "Disabling", "Updating", "Deleting":
				return kernel.ReadinessResult{Phase: kernel.ReadinessPending, Message: "event source mapping transition is still in progress"}
			default:
				return kernel.ReadinessResult{Phase: kernel.ReadinessFailed, Message: "event source mapping entered state " + observed.State}
			}
		},
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired EventSourceMappingSpec, outputs EventSourceMappingOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	if outputs.UUID != "" {
		return observeMapping(ctx, api, outputs.UUID)
	}
	if desired.FunctionName == "" || desired.EventSourceArn == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	uuid, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		return api.FindEventSourceMapping(rc, desired.FunctionName, desired.EventSourceArn)
	}, classifyESMObserve)
	if err != nil || uuid == "" {
		return kernel.Observation[ObservedState]{}, err
	}
	return observeMapping(ctx, api, uuid)
}
func (o *genericOperations) Create(ctx restate.ObjectContext, desired EventSourceMappingSpec) (kernel.CreateResult[EventSourceMappingOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[EventSourceMappingOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	outputs, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (EventSourceMappingOutputs, error) {
		uuid, findErr := api.FindEventSourceMapping(rc, desired.FunctionName, desired.EventSourceArn)
		if findErr != nil {
			return EventSourceMappingOutputs{}, findErr
		}
		if uuid != "" {
			observed, getErr := api.GetEventSourceMapping(rc, uuid)
			return outputsFromObserved(observed), getErr
		}
		return api.CreateEventSourceMapping(rc, desired)
	}, classifyESMMutation)
	return kernel.CreateResult[EventSourceMappingOutputs]{SeedOutputs: outputs}, err
}
func (o *genericOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next EventSourceMappingSpec, _ ObservedState) error {
	if previous.EventSourceArn != "" && previous.EventSourceArn != next.EventSourceArn || startingPositionChanged(previous, next) {
		return restate.TerminalError(fmt.Errorf("startingPosition, startingPositionTimestamp, and eventSourceArn are immutable for event source mappings"), 409)
	}
	return nil
}
func (o *genericOperations) Converge(ctx restate.ObjectContext, desired EventSourceMappingSpec, observed ObservedState) error {
	if desired.EventSourceArn != observed.EventSourceArn {
		return restate.TerminalError(fmt.Errorf("eventSourceArn is immutable; delete and reprovision the event source mapping"), 409)
	}
	if !HasDrift(desired, observed) {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	if _, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.UpdateEventSourceMapping(rc, observed.UUID, desired)
	}, classifyESMMutation); err != nil {
		return err
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) { return api.WaitForStableState(rc, observed.UUID) }, classifyESMMutation)
	return err
}
func (o *genericOperations) Delete(ctx restate.ObjectContext, desired EventSourceMappingSpec, outputs EventSourceMappingOutputs) error {
	if outputs.UUID == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	if _, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		e := api.DeleteEventSourceMapping(rc, outputs.UUID)
		if IsNotFound(e) {
			e = nil
		}
		return restate.Void{}, e
	}, classifyESMMutation); err != nil {
		return err
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		state, e := api.WaitForStableState(rc, outputs.UUID)
		if IsNotFound(e) {
			return "Deleted", nil
		}
		return state, e
	}, classifyESMMutation)
	return err
}
func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeMapping(ctx, api, ref.ResourceID)
}
func observeMapping(ctx restate.ObjectContext, api ESMAPI, uuid string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.GetEventSourceMapping(rc, uuid)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyESMObserve)
}
func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (ESMAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("event source mapping driver is not configured")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve event source mapping account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}
func classifyESMObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParameter(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}
func classifyESMMutation(err error) error {
	if err == nil || restate.IsTerminalError(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsConflict(err) {
		return restate.TerminalError(err, 409)
	}
	if IsInvalidParameter(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}
