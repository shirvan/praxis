package lambdalayer

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
	apiFactory func(aws.Config) LayerAPI
}

func NewGenericLambdaLayerDriver(auth authservice.AuthClient) *kernel.Driver[LambdaLayerSpec, LambdaLayerOutputs, ObservedState] {
	return newGenericLambdaLayerDriverWithFactory(auth, nil)
}
func newGenericLambdaLayerDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) LayerAPI) *kernel.Driver[LambdaLayerSpec, LambdaLayerOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) LayerAPI { return NewLayerAPI(awsclient.NewLambdaClient(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[LambdaLayerSpec, LambdaLayerOutputs, ObservedState]{ServiceName: ServiceName, Capabilities: kernel.Capabilities{Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true}, Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec LambdaLayerSpec) (LambdaLayerSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return LambdaLayerSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			if spec.Region == "" {
				spec.Region = region
			}
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateProvisionSpec,
		ValidateImport: func(spec LambdaLayerSpec) error {
			if spec.Region == "" || spec.LayerName == "" {
				return fmt.Errorf("region and layerName are required")
			}
			return nil
		},
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) LambdaLayerSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ LambdaLayerOutputs) LambdaLayerOutputs {
			return outputsFromObserved(observed)
		},
		FieldDiffs: func(desired LambdaLayerSpec, observed ObservedState) []types.FieldDiff {
			return ComputeFieldDiffs(desired, observed, LambdaLayerOutputs{Version: observed.expectedVersion})
		},
		HasDrift: func(desired LambdaLayerSpec, observed ObservedState) bool {
			return HasDrift(desired, observed, LambdaLayerOutputs{Version: observed.expectedVersion})
		},
	})
}
func (o *genericOperations) Observe(ctx restate.ObjectContext, desired LambdaLayerSpec, outputs LambdaLayerOutputs) (kernel.Observation[ObservedState], error) {
	name := outputs.LayerName
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	result, err := observeLayer(ctx, api, name)
	if result.Exists {
		result.Value.expectedVersion = outputs.Version
	}
	return result, err
}
func (o *genericOperations) Create(ctx restate.ObjectContext, desired LambdaLayerSpec) (kernel.CreateResult[LambdaLayerOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[LambdaLayerOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	outputs, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (LambdaLayerOutputs, error) { return api.PublishLayerVersion(rc, desired) }, classifyLayerMutation)
	return kernel.CreateResult[LambdaLayerOutputs]{SeedOutputs: outputs}, err
}
func (o *genericOperations) ConvergeProvisionChange(ctx restate.ObjectContext, previous, next LambdaLayerSpec, _ ObservedState) error {
	if previous.LayerName != "" && previous.LayerName != next.LayerName {
		return restate.TerminalError(fmt.Errorf("layerName is immutable; delete and reprovision the layer"), 409)
	}
	if !layerContentChanged(previous.Code, next.Code) && !layerMetadataChanged(previous, next) {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, next.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (LambdaLayerOutputs, error) { return api.PublishLayerVersion(rc, next) }, classifyLayerMutation)
	return err
}
func (o *genericOperations) Converge(ctx restate.ObjectContext, desired LambdaLayerSpec, observed ObservedState) error {
	if desired.LayerName != observed.LayerName {
		return restate.TerminalError(fmt.Errorf("layerName is immutable; delete and reprovision the layer"), 409)
	}
	if observed.expectedVersion != 0 && observed.Version != observed.expectedVersion {
		api, _, err := o.apiForAccount(ctx, desired.Account)
		if err != nil {
			return drivers.ClassifyCredentialError(err)
		}
		published, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (LambdaLayerOutputs, error) {
			return api.PublishLayerVersion(rc, desired)
		}, classifyLayerMutation)
		if err != nil {
			return err
		}
		_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (PermissionsSpec, error) {
			return api.SyncLayerVersionPermissions(rc, desired.LayerName, published.Version, desiredPermissions(desired))
		}, classifyLayerMutation)
		return err
	}
	desiredPerms := desiredPermissions(desired)
	if normalizePermissions(desiredPerms).Public == normalizePermissions(observed.Permissions).Public && sortedSlicesEqual(normalizePermissions(desiredPerms).AccountIds, normalizePermissions(observed.Permissions).AccountIds) {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (PermissionsSpec, error) {
		return api.SyncLayerVersionPermissions(rc, desired.LayerName, observed.Version, desiredPerms)
	}, classifyLayerMutation)
	return err
}
func (o *genericOperations) Delete(ctx restate.ObjectContext, desired LambdaLayerSpec, outputs LambdaLayerOutputs) error {
	name := outputs.LayerName
	if name == "" {
		name = desired.LayerName
	}
	if name == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		versions, e := api.ListLayerVersions(rc, name)
		if IsNotFound(e) {
			return restate.Void{}, nil
		}
		if e != nil {
			return restate.Void{}, e
		}
		for _, version := range versions {
			if e = api.DeleteLayerVersion(rc, name, version); e != nil && !IsNotFound(e) {
				return restate.Void{}, e
			}
		}
		return restate.Void{}, nil
	}, classifyLayerMutation)
	return err
}
func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeLayer(ctx, api, ref.ResourceID)
}
func observeLayer(ctx restate.ObjectContext, api LayerAPI, name string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.GetLatestLayerVersion(rc, name)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyLayerObserve)
}
func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (LayerAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("lambda layer driver is not configured")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve Lambda layer account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}
func classifyLayerObserve(err error) error {
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
func classifyLayerMutation(err error) error {
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
