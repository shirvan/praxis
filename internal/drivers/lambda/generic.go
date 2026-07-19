package lambda

import (
	"fmt"
	"slices"
	"time"

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
	apiFactory func(aws.Config) LambdaAPI
}

func NewGenericLambdaFunctionDriver(auth authservice.AuthClient) *kernel.Driver[LambdaFunctionSpec, LambdaFunctionOutputs, ObservedState] {
	return newGenericLambdaFunctionDriverWithFactory(auth, nil)
}

func newGenericLambdaFunctionDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) LambdaAPI) *kernel.Driver[LambdaFunctionSpec, LambdaFunctionOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) LambdaAPI { return NewLambdaAPI(awsclient.NewLambdaClient(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[LambdaFunctionSpec, LambdaFunctionOutputs, ObservedState]{
		ServiceName:  ServiceName,
		Capabilities: kernel.Capabilities{Declared: true, Import: true, ObservedMode: true, Delete: true, Readiness: true},
		Operations:   ops,
		Prepare: func(ctx restate.ObjectContext, spec LambdaFunctionSpec) (LambdaFunctionSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return LambdaFunctionSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			if spec.Region == "" {
				spec.Region = region
			}
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate: validateProvisionSpec,
		ValidateImport: func(spec LambdaFunctionSpec) error {
			code := spec.Code
			spec.Code = CodeSpec{ImageURI: "import-placeholder"}
			err := validateProvisionSpec(spec)
			spec.Code = code
			return err
		},
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) LambdaFunctionSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ LambdaFunctionOutputs) LambdaFunctionOutputs {
			return outputsFromObserved(observed)
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
		CheckReadiness: func(observed ObservedState) kernel.ReadinessResult {
			if observed.State == "Failed" || observed.LastUpdateStatus == "Failed" {
				return kernel.ReadinessResult{Phase: kernel.ReadinessFailed, Message: "Lambda function entered a failed state"}
			}
			if (observed.State == "" || observed.State == "Active") && (observed.LastUpdateStatus == "" || observed.LastUpdateStatus == "Successful") {
				return kernel.ReadinessResult{Phase: kernel.ReadinessReady}
			}
			return kernel.ReadinessResult{Phase: kernel.ReadinessPending, Message: "Lambda function update is still in progress"}
		},
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired LambdaFunctionSpec, outputs LambdaFunctionOutputs) (kernel.Observation[ObservedState], error) {
	name := outputs.FunctionName
	if name == "" {
		name = desired.FunctionName
	}
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	observation, err := observeFunction(ctx, api, name)
	if err != nil || !observation.Exists || outputs.FunctionName != "" {
		return observation, err
	}
	if observation.Value.Tags["praxis:managed-key"] != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("refusing to adopt Lambda function %q without exact Praxis ownership tag %q", name, desired.ManagedKey), 409)
	}
	return observation, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired LambdaFunctionSpec) (kernel.CreateResult[LambdaFunctionOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[LambdaFunctionOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	arn, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		observed, describeErr := api.DescribeFunction(rc, desired.FunctionName)
		if describeErr == nil {
			if observed.Tags["praxis:managed-key"] != desired.ManagedKey {
				return "", restate.TerminalError(fmt.Errorf("lambda function %q already exists without exact Praxis ownership", desired.FunctionName), 409)
			}
			return observed.FunctionArn, nil
		}
		if !IsNotFound(describeErr) {
			return "", describeErr
		}
		return api.CreateFunction(rc, desired)
	}, classifyLambdaMutation)
	return kernel.CreateResult[LambdaFunctionOutputs]{SeedOutputs: LambdaFunctionOutputs{FunctionArn: arn, FunctionName: desired.FunctionName}}, err
}

func (o *genericOperations) ConvergeProvisionChange(ctx restate.ObjectContext, previous, next LambdaFunctionSpec, observed ObservedState, currentOutputs LambdaFunctionOutputs) (LambdaFunctionOutputs, error) {
	if previous.PackageType != "" && previous.PackageType != next.PackageType {
		return currentOutputs, restate.TerminalError(fmt.Errorf("packageType is immutable; delete and recreate the function to change it"), 409)
	}
	if previous.FunctionName != "" && previous.FunctionName != next.FunctionName {
		return currentOutputs, restate.TerminalError(fmt.Errorf("functionName is immutable; delete and recreate the function to change it"), 409)
	}
	if !slices.Equal(normalizeArchitectures(previous.Architectures), normalizeArchitectures(next.Architectures)) {
		return currentOutputs, restate.TerminalError(fmt.Errorf("architectures are immutable; delete and recreate the function to change them"), 409)
	}
	if !codeSpecChanged(previous.Code, next.Code) {
		return currentOutputs, nil
	}
	api, _, err := o.apiForAccount(ctx, next.Account)
	if err != nil {
		return currentOutputs, drivers.ClassifyCredentialError(err)
	}
	if _, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.UpdateFunctionCode(rc, next)
	}, classifyLambdaMutation); err != nil {
		return currentOutputs, err
	}
	return currentOutputs, waitFunctionStable(ctx, api, observed.FunctionName)
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired LambdaFunctionSpec, observed ObservedState, currentOutputs LambdaFunctionOutputs) (LambdaFunctionOutputs, error) {
	if desired.FunctionName != observed.FunctionName || (observed.PackageType != "" && desired.PackageType != observed.PackageType) || !slices.Equal(normalizeArchitectures(desired.Architectures), normalizeArchitectures(observed.Architectures)) {
		return currentOutputs, restate.TerminalError(fmt.Errorf("lambda function has immutable changes; delete and reprovision it"), 409)
	}
	if owner := observed.Tags["praxis:managed-key"]; owner != "" && owner != desired.ManagedKey {
		return currentOutputs, restate.TerminalError(fmt.Errorf("lambda function is owned by Praxis object %q", owner), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return currentOutputs, drivers.ClassifyCredentialError(err)
	}
	if HasDrift(desired, observed) {
		if _, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateFunctionConfiguration(rc, desired, observed)
		}, classifyLambdaMutation); err != nil {
			return currentOutputs, err
		}
		if err := waitFunctionStable(ctx, api, desired.FunctionName); err != nil {
			return currentOutputs, err
		}
	}
	if !tagsEqual(desired.Tags, observed.Tags) || observed.Tags["praxis:managed-key"] != desired.ManagedKey {
		_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.FunctionArn, withManagedKey(desired.ManagedKey, desired.Tags))
		}, classifyLambdaMutation)
	}
	return currentOutputs, err
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired LambdaFunctionSpec, outputs LambdaFunctionOutputs) error {
	name := outputs.FunctionName
	if name == "" {
		name = desired.FunctionName
	}
	if name == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	observed, err := observeFunction(ctx, api, name)
	if err != nil || !observed.Exists {
		return err
	}
	if owner := observed.Value.Tags["praxis:managed-key"]; owner != "" && owner != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf("refusing to delete Lambda function owned by %q", owner), 409)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		e := api.DeleteFunction(rc, name)
		if IsNotFound(e) {
			e = nil
		}
		return restate.Void{}, e
	}, classifyLambdaMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeFunction(ctx, api, ref.ResourceID)
}

func observeFunction(ctx restate.ObjectContext, api LambdaAPI, name string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeFunction(rc, name)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyLambdaObserve)
}
func waitFunctionStable(ctx restate.ObjectContext, api LambdaAPI, name string) error {
	_, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.WaitForFunctionStable(rc, name, 2*time.Minute)
	}, classifyLambdaMutation)
	return err
}
func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (LambdaAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("lambda function driver is not configured")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve Lambda account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}
func classifyLambdaObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParameter(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}
func classifyLambdaMutation(err error) error {
	if err == nil || restate.IsTerminalError(err) {
		return err
	}
	if IsAccessDenied(err) {
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
