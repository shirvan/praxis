package lambdalayer

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// LambdaLayerDriver implements the Praxis driver for AWS Lambda Layers.
type LambdaLayerDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) LayerAPI
}

// NewLambdaLayerDriver creates a production driver with default Lambda client factory.
func NewLambdaLayerDriver(auth authservice.AuthClient) *LambdaLayerDriver {
	return NewLambdaLayerDriverWithFactory(auth, func(cfg aws.Config) LayerAPI {
		return NewLayerAPI(awsclient.NewLambdaClient(cfg))
	})
}

// NewLambdaLayerDriverWithFactory creates a driver with a custom LayerAPI factory (for testing).
func NewLambdaLayerDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) LayerAPI) *LambdaLayerDriver {
	if factory == nil {
		factory = func(cfg aws.Config) LayerAPI { return NewLayerAPI(awsclient.NewLambdaClient(cfg)) }
	}
	return &LambdaLayerDriver{auth: auth, apiFactory: factory}
}

func (d *LambdaLayerDriver) ServiceName() string { return ServiceName }

// Provision creates or updates a Lambda Layer.
//
// Flow:
//  1. Validate required fields, apply defaults.
//  2. If a version already exists and no content/metadata changed,
//     only sync permissions — skip re-publishing.
//  3. If content or metadata changed: PublishLayerVersion (creates new immutable version),
//     then sync permissions on the new version.
//  4. Final GetLatestLayerVersion to capture outputs. Set status=Ready.
func (d *LambdaLayerDriver) Provision(ctx restate.ObjectContext, spec LambdaLayerSpec) (LambdaLayerOutputs, error) {
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return LambdaLayerOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	if spec.Region == "" {
		spec.Region = region
	}
	if err := validateProvisionSpec(spec); err != nil {
		return LambdaLayerOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[LambdaLayerState](ctx, drivers.StateKey)
	if err != nil {
		return LambdaLayerOutputs{}, err
	}
	changed := layerContentChanged(state.Desired.Code, spec.Code) || layerMetadataChanged(state.Desired, spec)
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	if state.Outputs.LayerVersionArn != "" && !changed {
		permissions, syncErr := restate.Run(ctx, func(rc restate.RunContext) (PermissionsSpec, error) {
			return api.SyncLayerVersionPermissions(rc, spec.LayerName, state.Outputs.Version, desiredPermissions(spec))
		})
		if syncErr != nil {
			state.Status = types.StatusError
			state.Error = syncErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return LambdaLayerOutputs{}, syncErr
		}
		state.Observed.Permissions = permissions
		state.Status = types.StatusReady
		state.Error = ""
		state.Outputs.LayerName = spec.LayerName
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return state.Outputs, nil
	}

	outputs, err := restate.Run(ctx, func(rc restate.RunContext) (LambdaLayerOutputs, error) {
		return api.PublishLayerVersion(rc, spec)
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		if IsInvalidParameter(err) {
			return LambdaLayerOutputs{}, restate.TerminalError(err, 400)
		}
		return LambdaLayerOutputs{}, err
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.GetLatestLayerVersion(rc, spec.LayerName)
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = outputs
		restate.Set(ctx, drivers.StateKey, state)
		return LambdaLayerOutputs{}, err
	}
	permissions, err := restate.Run(ctx, func(rc restate.RunContext) (PermissionsSpec, error) {
		return api.SyncLayerVersionPermissions(rc, spec.LayerName, observed.Version, desiredPermissions(spec))
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = outputs
		restate.Set(ctx, drivers.StateKey, state)
		return LambdaLayerOutputs{}, err
	}
	observed.Permissions = permissions
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Import adopts an existing Lambda Layer (latest version) into Praxis management.
func (d *LambdaLayerDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (LambdaLayerOutputs, error) {
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return LambdaLayerOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[LambdaLayerState](ctx, drivers.StateKey)
	if err != nil {
		return LambdaLayerOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.GetLatestLayerVersion(rc, ref.ResourceID)
	})
	if err != nil {
		if IsNotFound(err) {
			return LambdaLayerOutputs{}, restate.TerminalError(fmt.Errorf("import failed: Lambda layer %s does not exist", ref.ResourceID), 404)
		}
		return LambdaLayerOutputs{}, err
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Mode = defaultImportMode(ref.Mode)
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Delete removes ALL versions of the Lambda Layer.
// Observed-mode resources cannot be deleted (409).
func (d *LambdaLayerDriver) Delete(ctx restate.ObjectContext) error {
	state, err := restate.Get[LambdaLayerState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete Lambda layer %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.LayerName), 409)
	}
	if state.Outputs.LayerName == "" && state.Desired.LayerName == "" {
		restate.Set(ctx, drivers.StateKey, LambdaLayerState{Status: types.StatusDeleted})
		return nil
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}
	layerName := state.Outputs.LayerName
	if layerName == "" {
		layerName = state.Desired.LayerName
	}
	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		versions, runErr := api.ListLayerVersions(rc, layerName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsInvalidParameter(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
			}
			if drivers.IsAccessDenied(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 403)
			}
			return restate.Void{}, runErr
		}
		for _, version := range versions {
			if deleteErr := api.DeleteLayerVersion(rc, layerName, version); deleteErr != nil && !IsNotFound(deleteErr) {
				if IsInvalidParameter(deleteErr) {
					return restate.Void{}, restate.TerminalError(deleteErr, 400)
				}
				if drivers.IsAccessDenied(deleteErr) {
					return restate.Void{}, restate.TerminalError(deleteErr, 403)
				}
				return restate.Void{}, deleteErr
			}
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}
	restate.Set(ctx, drivers.StateKey, LambdaLayerState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-detection loop for Lambda Layers.
// Since layer versions are immutable, Reconcile detects-only:
// external deletion, external version publishes, and permission drift.
// No automatic correction is performed.
func (d *LambdaLayerDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[LambdaLayerState](ctx, drivers.StateKey)
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
	if state.Outputs.LayerName == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) { return time.Now().UTC().Format(time.RFC3339), nil })
	if err != nil {
		return types.ReconcileResult{}, err
	}
	previousOutputs := state.Outputs
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.GetLatestLayerVersion(rc, state.Outputs.LayerName)
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("Lambda layer %s was deleted externally", state.Outputs.LayerName)
			state.LastReconcile = now
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventExternalDelete, state.Error)
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
	drift := HasDrift(state.Desired, observed, previousOutputs)
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	if drift {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
	}
	return types.ReconcileResult{Drift: drift}, nil
}

// GetStatus returns the current lifecycle status (shared/concurrent handler).
func (d *LambdaLayerDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[LambdaLayerState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs returns the provisioned outputs (shared/concurrent handler).
func (d *LambdaLayerDriver) GetOutputs(ctx restate.ObjectSharedContext) (LambdaLayerOutputs, error) {
	state, err := restate.Get[LambdaLayerState](ctx, drivers.StateKey)
	if err != nil {
		return LambdaLayerOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *LambdaLayerDriver) GetInputs(ctx restate.ObjectSharedContext) (LambdaLayerSpec, error) {
	state, err := restate.Get[LambdaLayerState](ctx, drivers.StateKey)
	if err != nil {
		return LambdaLayerSpec{}, err
	}
	return state.Desired, nil
}

// scheduleReconcile enqueues a delayed Reconcile message with dedup guard.
func (d *LambdaLayerDriver) scheduleReconcile(ctx restate.ObjectContext, state *LambdaLayerState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileIntervalForKind(ServiceName)))
}

// apiForAccount resolves AWS credentials and creates a LayerAPI for the given Praxis account.
func (d *LambdaLayerDriver) apiForAccount(ctx restate.ObjectContext, account string) (LayerAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("LambdaLayerDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve Lambda layer account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

// applyDefaults normalizes permissions, runtimes, and architectures.
func applyDefaults(spec LambdaLayerSpec) LambdaLayerSpec {
	if spec.Permissions == nil {
		spec.Permissions = &PermissionsSpec{}
	} else {
		permissions := normalizePermissions(*spec.Permissions)
		spec.Permissions = &permissions
	}
	if spec.CompatibleRuntimes == nil {
		spec.CompatibleRuntimes = []string{}
	} else {
		spec.CompatibleRuntimes = append([]string(nil), spec.CompatibleRuntimes...)
		slices.Sort(spec.CompatibleRuntimes)
	}
	if spec.CompatibleArchitectures == nil {
		spec.CompatibleArchitectures = []string{}
	} else {
		spec.CompatibleArchitectures = append([]string(nil), spec.CompatibleArchitectures...)
		slices.Sort(spec.CompatibleArchitectures)
	}
	return spec
}

// validateProvisionSpec checks that region, layerName, and code source are set.
func validateProvisionSpec(spec LambdaLayerSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.LayerName) == "" {
		return fmt.Errorf("layerName is required")
	}
	return validateCode(spec.Code)
}

// layerContentChanged returns true if the code deployment artifact differs.
func layerContentChanged(a, b CodeSpec) bool {
	if (a.S3 == nil) != (b.S3 == nil) {
		return true
	}
	if a.S3 != nil && b.S3 != nil && *a.S3 != *b.S3 {
		return true
	}
	return a.ZipFile != b.ZipFile
}

// layerMetadataChanged returns true if description, license, runtimes, or architectures differ.
func layerMetadataChanged(a, b LambdaLayerSpec) bool {
	return a.Description != b.Description ||
		a.LicenseInfo != b.LicenseInfo ||
		!slices.Equal(a.CompatibleRuntimes, b.CompatibleRuntimes) ||
		!slices.Equal(a.CompatibleArchitectures, b.CompatibleArchitectures)
}

// specFromObserved reconstructs a LambdaLayerSpec from observed AWS state for Import.
func specFromObserved(observed ObservedState) LambdaLayerSpec {
	permissions := observed.Permissions
	return applyDefaults(LambdaLayerSpec{LayerName: observed.LayerName, Description: observed.Description, CompatibleRuntimes: append([]string(nil), observed.CompatibleRuntimes...), CompatibleArchitectures: append([]string(nil), observed.CompatibleArchitectures...), LicenseInfo: observed.LicenseInfo, Permissions: &permissions})
}

// outputsFromObserved maps ObservedState to user-facing LambdaLayerOutputs.
func outputsFromObserved(observed ObservedState) LambdaLayerOutputs {
	return LambdaLayerOutputs{LayerArn: observed.LayerArn, LayerVersionArn: observed.LayerVersionArn, LayerName: observed.LayerName, Version: observed.Version, CodeSize: observed.CodeSize, CodeSha256: observed.CodeSha256, CreatedDate: observed.CreatedDate}
}

// defaultImportMode returns Observed if no mode was explicitly specified.
func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

// desiredPermissions extracts and normalizes permissions from the spec.
func desiredPermissions(spec LambdaLayerSpec) PermissionsSpec {
	if spec.Permissions == nil {
		return PermissionsSpec{}
	}
	return normalizePermissions(*spec.Permissions)
}

// ClearState clears all Virtual Object state for this resource.
// Used by the Orphan deletion policy to release a resource from management.
func (d *LambdaLayerDriver) ClearState(ctx restate.ObjectContext) error {
	drivers.ClearAllState(ctx)
	return nil
}
