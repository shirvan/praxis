package lambda

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type LambdaFunctionDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) LambdaAPI
}

func NewLambdaFunctionDriver(auth authservice.AuthClient) *LambdaFunctionDriver {
	return NewLambdaFunctionDriverWithFactory(auth, func(cfg aws.Config) LambdaAPI {
		return NewLambdaAPI(awsclient.NewLambdaClient(cfg))
	})
}

func NewLambdaFunctionDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) LambdaAPI) *LambdaFunctionDriver {
	if factory == nil {
		factory = func(cfg aws.Config) LambdaAPI { return NewLambdaAPI(awsclient.NewLambdaClient(cfg)) }
	}
	return &LambdaFunctionDriver{auth: auth, apiFactory: factory}
}

func (d *LambdaFunctionDriver) ServiceName() string {
	return ServiceName
}

func (d *LambdaFunctionDriver) Provision(ctx restate.ObjectContext, spec LambdaFunctionSpec) (LambdaFunctionOutputs, error) {
	ctx.Log().Info("provisioning lambda function", "key", restate.Key(ctx), "functionName", spec.FunctionName)
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return LambdaFunctionOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	if spec.Region == "" {
		spec.Region = region
	}
	if err := validateProvisionSpec(spec); err != nil {
		return LambdaFunctionOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[LambdaFunctionState](ctx, drivers.StateKey)
	if err != nil {
		return LambdaFunctionOutputs{}, err
	}
	previousDesired := state.Desired
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	observed, found, err := d.describeExisting(ctx, api, spec.FunctionName)
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return LambdaFunctionOutputs{}, err
	}

	if !found {
		_, err = restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			arn, runErr := api.CreateFunction(rc, spec)
			if runErr != nil {
				if IsInvalidParameter(runErr) {
					return "", restate.TerminalError(runErr, 400)
				}
				if IsConflict(runErr) {
					return "", restate.TerminalError(runErr, 409)
				}
				if IsAccessDenied(runErr) {
					return "", restate.TerminalError(runErr, 403)
				}
				return "", runErr
			}
			return arn, nil
		})
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return LambdaFunctionOutputs{}, err
		}
		if err := d.waitStable(ctx, api, spec.FunctionName); err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return LambdaFunctionOutputs{}, err
		}
	} else {
		if previousDesired.PackageType != "" && previousDesired.PackageType != spec.PackageType {
			return LambdaFunctionOutputs{}, restate.TerminalError(fmt.Errorf("packageType is immutable; delete and recreate the function to change it"), 409)
		}
		codeChanged := codeSpecChanged(previousDesired.Code, spec.Code)
		configChanged := HasDrift(spec, observed)
		if codeChanged {
			_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				runErr := api.UpdateFunctionCode(rc, spec)
				if runErr != nil {
					if IsInvalidParameter(runErr) {
						return restate.Void{}, restate.TerminalError(runErr, 400)
					}
					if IsConflict(runErr) {
						return restate.Void{}, restate.TerminalError(runErr, 409)
					}
					return restate.Void{}, runErr
				}
				return restate.Void{}, nil
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return LambdaFunctionOutputs{}, err
			}
			if err := d.waitStable(ctx, api, spec.FunctionName); err != nil {
				state.Status = types.StatusError
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return LambdaFunctionOutputs{}, err
			}
		}
		if configChanged {
			_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				runErr := api.UpdateFunctionConfiguration(rc, spec, observed)
				if runErr != nil {
					if IsInvalidParameter(runErr) {
						return restate.Void{}, restate.TerminalError(runErr, 400)
					}
					if IsConflict(runErr) {
						return restate.Void{}, restate.TerminalError(runErr, 409)
					}
					return restate.Void{}, runErr
				}
				return restate.Void{}, nil
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return LambdaFunctionOutputs{}, err
			}
			if err := d.waitStable(ctx, api, spec.FunctionName); err != nil {
				state.Status = types.StatusError
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return LambdaFunctionOutputs{}, err
			}
		}
		if !tagsEqual(spec.Tags, observed.Tags) {
			_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				runErr := api.UpdateTags(rc, observed.FunctionArn, spec.Tags)
				if runErr != nil {
					return restate.Void{}, runErr
				}
				return restate.Void{}, nil
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return LambdaFunctionOutputs{}, err
			}
		}
	}

	observed, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeFunction(rc, spec.FunctionName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return LambdaFunctionOutputs{}, err
	}
	outputs := outputsFromObserved(observed)
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

func (d *LambdaFunctionDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (LambdaFunctionOutputs, error) {
	ctx.Log().Info("importing lambda function", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return LambdaFunctionOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[LambdaFunctionState](ctx, drivers.StateKey)
	if err != nil {
		return LambdaFunctionOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeFunction(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: Lambda function %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return LambdaFunctionOutputs{}, err
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region
	outputs := outputsFromObserved(observed)
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Mode = defaultLambdaImportMode(ref.Mode)
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

func (d *LambdaFunctionDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting lambda function", "key", restate.Key(ctx))
	state, err := restate.Get[LambdaFunctionState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete Lambda function %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.FunctionName), 409)
	}
	name := state.Desired.FunctionName
	if name == "" {
		name = state.Outputs.FunctionName
	}
	if name == "" {
		restate.Set(ctx, drivers.StateKey, LambdaFunctionState{Status: types.StatusDeleted})
		return nil
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}
	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteFunction(rc, name)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			return restate.Void{}, runErr
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}
	restate.Set(ctx, drivers.StateKey, LambdaFunctionState{Status: types.StatusDeleted})
	return nil
}

func (d *LambdaFunctionDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[LambdaFunctionState](ctx, drivers.StateKey)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return types.ReconcileResult{}, restate.TerminalError(err, 400)
	}
	state.ReconcileScheduled = false
	if state.Status != types.StatusReady && state.Status != types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	name := state.Outputs.FunctionName
	if name == "" {
		name = state.Desired.FunctionName
	}
	if name == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return types.ReconcileResult{}, err
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeFunction(rc, name)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("Lambda function %s was deleted externally", name)
			state.LastReconcile = now
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Error: state.Error}, nil
		}
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed)
	if drift && state.Mode == types.ModeManaged {
		if correctionErr := d.correctDrift(ctx, api, state.Desired, observed); correctionErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}
	if drift && state.Mode == types.ModeObserved {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true}, nil
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *LambdaFunctionDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[LambdaFunctionState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *LambdaFunctionDriver) GetOutputs(ctx restate.ObjectSharedContext) (LambdaFunctionOutputs, error) {
	state, err := restate.Get[LambdaFunctionState](ctx, drivers.StateKey)
	if err != nil {
		return LambdaFunctionOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *LambdaFunctionDriver) correctDrift(ctx restate.ObjectContext, api LambdaAPI, desired LambdaFunctionSpec, observed ObservedState) error {
	if HasDrift(desired, observed) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.UpdateFunctionConfiguration(rc, desired, observed)
			if runErr != nil {
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("update Lambda configuration: %w", err)
		}
		if err := d.waitStable(ctx, api, desired.FunctionName); err != nil {
			return err
		}
	}
	if !tagsEqual(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.UpdateTags(rc, observed.FunctionArn, desired.Tags)
			if runErr != nil {
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("update Lambda tags: %w", err)
		}
	}
	return nil
}

func (d *LambdaFunctionDriver) scheduleReconcile(ctx restate.ObjectContext, state *LambdaFunctionState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *LambdaFunctionDriver) apiForAccount(ctx restate.ObjectContext, account string) (LambdaAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("LambdaFunctionDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve Lambda account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func (d *LambdaFunctionDriver) waitStable(ctx restate.ObjectContext, api LambdaAPI, functionName string) error {
	_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if runErr := api.WaitForFunctionStable(rc, functionName, 2*time.Minute); runErr != nil {
			return restate.Void{}, runErr
		}
		return restate.Void{}, nil
	})
	return err
}

func (d *LambdaFunctionDriver) describeExisting(ctx restate.ObjectContext, api LambdaAPI, functionName string) (ObservedState, bool, error) {
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeFunction(rc, functionName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, nil
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return ObservedState{}, false, err
	}
	return observed, observed.FunctionArn != "", nil
}

func outputsFromObserved(observed ObservedState) LambdaFunctionOutputs {
	return LambdaFunctionOutputs{FunctionArn: observed.FunctionArn, FunctionName: observed.FunctionName, Version: observed.Version, State: observed.State, LastModified: observed.LastModified, LastUpdateStatus: observed.LastUpdateStatus, CodeSha256: observed.CodeSha256}
}

func specFromObserved(observed ObservedState) LambdaFunctionSpec {
	spec := applyDefaults(LambdaFunctionSpec{FunctionName: observed.FunctionName, Role: observed.Role, PackageType: observed.PackageType, Runtime: observed.Runtime, Handler: observed.Handler, Description: observed.Description, MemorySize: observed.MemorySize, Timeout: observed.Timeout, Environment: observed.Environment, Layers: append([]string(nil), observed.Layers...), Tags: filterPraxisTags(observed.Tags)})
	if len(observed.VpcConfig.SubnetIds) > 0 || len(observed.VpcConfig.SecurityGroupIds) > 0 {
		spec.VPCConfig = &VPCConfigSpec{SubnetIds: append([]string(nil), observed.VpcConfig.SubnetIds...), SecurityGroupIds: append([]string(nil), observed.VpcConfig.SecurityGroupIds...)}
	}
	if observed.DeadLetterTarget != "" {
		spec.DeadLetterConfig = &DeadLetterConfigSpec{TargetArn: observed.DeadLetterTarget}
	}
	if observed.TracingMode != "" {
		spec.TracingConfig = &TracingConfigSpec{Mode: observed.TracingMode}
	}
	if len(observed.Architectures) > 0 {
		spec.Architectures = append([]string(nil), observed.Architectures...)
	}
	if observed.EphemeralSize > 0 {
		spec.EphemeralStorage = &EphemeralStorageSpec{Size: observed.EphemeralSize}
	}
	if observed.ImageURI != "" {
		spec.Code.ImageURI = observed.ImageURI
	}
	return spec
}

func applyDefaults(spec LambdaFunctionSpec) LambdaFunctionSpec {
	if spec.MemorySize == 0 {
		spec.MemorySize = 128
	}
	if spec.Timeout == 0 {
		spec.Timeout = 3
	}
	if spec.PackageType == "" {
		if spec.Code.ImageURI != "" {
			spec.PackageType = "Image"
		} else {
			spec.PackageType = "Zip"
		}
	}
	if len(spec.Architectures) == 0 {
		spec.Architectures = []string{"x86_64"}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateProvisionSpec(spec LambdaFunctionSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.FunctionName == "" {
		return fmt.Errorf("functionName is required")
	}
	if spec.Role == "" {
		return fmt.Errorf("role is required")
	}
	if spec.PackageType != "Image" {
		if spec.Runtime == "" {
			return fmt.Errorf("runtime is required for Zip functions")
		}
		if spec.Handler == "" {
			return fmt.Errorf("handler is required for Zip functions")
		}
	}
	return validateCode(spec.Code)
}

func defaultLambdaImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}
