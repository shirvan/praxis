// Package ssmparameter – driver.go
//
// This file implements the Restate Virtual Object handler for AWS SSM parameters.
// The driver exposes five durable handlers:
//   - Provision: create-or-update the resource and persist state
//   - Import:    adopt an existing AWS resource into Praxis management
//   - Delete:    remove the resource from AWS (managed mode only)
//   - Reconcile: periodic drift check + auto-correction (managed mode)
//   - GetStatus / GetOutputs: read-only shared handlers for status queries
//
// All mutating AWS calls are wrapped in restate.Run for durable execution,
// and reconciliation is self-scheduled via delayed Restate messages.
package ssmparameter

import (
	"fmt"
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

// SSMParameterDriver is the Restate Virtual Object handler for AWS SSM parameters.
// It holds an auth client (for cross-account credential resolution)
// and an API factory (swappable for testing).
type SSMParameterDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) SSMParameterAPI
}

// NewSSMParameterDriver creates an SSMParameter driver wired to the given
// auth client. It uses the default AWS SDK client factory.
func NewSSMParameterDriver(auth authservice.AuthClient) *SSMParameterDriver {
	return NewSSMParameterDriverWithFactory(auth, func(cfg aws.Config) SSMParameterAPI {
		return NewSSMParameterAPI(awsclient.NewSSMClient(cfg))
	})
}

// NewSSMParameterDriverWithFactory creates an SSMParameter driver with a custom
// API factory, primarily used in tests to inject mock AWS clients.
func NewSSMParameterDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) SSMParameterAPI) *SSMParameterDriver {
	if factory == nil {
		factory = func(cfg aws.Config) SSMParameterAPI {
			return NewSSMParameterAPI(awsclient.NewSSMClient(cfg))
		}
	}
	return &SSMParameterDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object service name for registration.
func (d *SSMParameterDriver) ServiceName() string {
	return ServiceName
}

// Provision creates or updates an SSM parameter. It validates the spec,
// checks for an existing parameter, and either creates a new one or
// overwrites diverged fields on the existing one. State is persisted in
// Restate K/V after every step.
func (d *SSMParameterDriver) Provision(ctx restate.ObjectContext, spec SSMParameterSpec) (SSMParameterOutputs, error) {
	ctx.Log().Info("provisioning SSM parameter", "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return SSMParameterOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	spec.Region = region
	spec.ManagedKey = restate.Key(ctx)
	if err := validateSpec(spec); err != nil {
		return SSMParameterOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[SSMParameterState](ctx, drivers.StateKey)
	if err != nil {
		return SSMParameterOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	observed, found, err := d.observeParameter(ctx, api, spec.ParameterName)
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return SSMParameterOutputs{}, err
	}

	if !found {
		_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			_, runErr := api.PutParameter(rc, spec, false)
			if runErr != nil {
				if IsAlreadyExists(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 409)
				}
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				if IsLimitExceeded(runErr) {
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
			return SSMParameterOutputs{}, err
		}
		observed, found, err = d.observeParameter(ctx, api, spec.ParameterName)
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return SSMParameterOutputs{}, err
		}
		if !found {
			err := fmt.Errorf("parameter %s was not found after creation", spec.ParameterName)
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return SSMParameterOutputs{}, err
		}
	}

	if err := d.convergeMutableFields(ctx, api, spec, observed, found); err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return SSMParameterOutputs{}, err
	}

	observed, found, err = d.observeParameter(ctx, api, spec.ParameterName)
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return SSMParameterOutputs{}, err
	}
	if !found {
		err := fmt.Errorf("parameter %s disappeared during provisioning", spec.ParameterName)
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return SSMParameterOutputs{}, err
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

// Import adopts an existing SSM parameter into Praxis management.
// It reads the current configuration from AWS, synthesizes a spec from
// the observed state, and stores it. Default import mode is Observed
// (read-only); users can re-import with --mode managed to enable writes.
func (d *SSMParameterDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (SSMParameterOutputs, error) {
	ctx.Log().Info("importing SSM parameter", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return SSMParameterOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[SSMParameterState](ctx, drivers.StateKey)
	if err != nil {
		return SSMParameterOutputs{}, err
	}
	state.Generation++
	observed, found, err := d.observeParameter(ctx, api, ref.ResourceID)
	if err != nil {
		return SSMParameterOutputs{}, err
	}
	if !found {
		return SSMParameterOutputs{}, restate.TerminalError(fmt.Errorf("import failed: parameter %s does not exist", ref.ResourceID), 404)
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region
	spec.ManagedKey = restate.Key(ctx)
	outputs := outputsFromObserved(observed)
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Mode = defaultImportMode(ref.Mode)
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Delete removes the SSM parameter from AWS. It is blocked for
// resources in Observed mode. The method handles not-found gracefully
// (idempotent delete) and sets the final state to StatusDeleted.
func (d *SSMParameterDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting SSM parameter", "key", restate.Key(ctx))
	state, err := restate.Get[SSMParameterState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete parameter %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.ParameterName), 409)
	}
	if state.Outputs.ParameterName == "" {
		restate.Set(ctx, drivers.StateKey, SSMParameterState{Status: types.StatusDeleted})
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
		runErr := api.DeleteParameter(rc, state.Outputs.ParameterName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsInvalidParam(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
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
	restate.Set(ctx, drivers.StateKey, SSMParameterState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-check handler. It re-reads the
// resource from AWS, compares against desired state, and auto-corrects
// drift when in Managed mode. In Observed mode it only reports drift.
// External deletions are detected and flagged as errors.
// The handler self-schedules via a delayed Restate message.
func (d *SSMParameterDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[SSMParameterState](ctx, drivers.StateKey)
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
	if state.Outputs.ParameterName == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return types.ReconcileResult{}, err
	}
	observed, found, err := d.observeParameter(ctx, api, state.Outputs.ParameterName)
	if err != nil {
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}
	if !found {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("parameter %s was deleted externally", state.Outputs.ParameterName)
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventExternalDelete, state.Error)
		return types.ReconcileResult{Error: state.Error}, nil
	}
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed)
	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift}, nil
	}
	if drift && state.Mode == types.ModeManaged {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		if correctionErr := d.convergeMutableFields(ctx, api, state.Desired, observed, true); correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}
	if drift && state.Mode == types.ModeObserved {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{Drift: drift}, nil
}

// GetStatus is a shared (read-only) handler that returns the current lifecycle status.
func (d *SSMParameterDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[SSMParameterState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a shared (read-only) handler that returns the provisioned resource outputs.
func (d *SSMParameterDriver) GetOutputs(ctx restate.ObjectSharedContext) (SSMParameterOutputs, error) {
	state, err := restate.Get[SSMParameterState](ctx, drivers.StateKey)
	if err != nil {
		return SSMParameterOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *SSMParameterDriver) GetInputs(ctx restate.ObjectSharedContext) (SSMParameterSpec, error) {
	state, err := restate.Get[SSMParameterState](ctx, drivers.StateKey)
	if err != nil {
		return SSMParameterSpec{}, err
	}
	return state.Desired, nil
}

func (d *SSMParameterDriver) convergeMutableFields(ctx restate.ObjectContext, api SSMParameterAPI, spec SSMParameterSpec, observed ObservedState, found bool) error {
	if found && parameterFieldsDrift(spec, observed) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			_, runErr := api.PutParameter(rc, spec, true)
			if runErr != nil {
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				if IsLimitExceeded(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 409)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return err
		}
	}
	if found {
		toAdd, toRemove := tagDiff(spec.Tags, observed.Tags, spec.ManagedKey)
		if len(toRemove) > 0 {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.RemoveTags(rc, spec.ParameterName, toRemove)
			})
			if err != nil {
				return err
			}
		}
		if len(toAdd) > 0 {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.AddTags(rc, spec.ParameterName, toAdd)
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *SSMParameterDriver) scheduleReconcile(ctx restate.ObjectContext, state *SSMParameterState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileIntervalForKind(ServiceName)))
}

func (d *SSMParameterDriver) apiForAccount(ctx restate.ObjectContext, account string) (SSMParameterAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("SSMParameterDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve SSMParameter account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func (d *SSMParameterDriver) observeParameter(ctx restate.ObjectContext, api SSMParameterAPI, name string) (ObservedState, bool, error) {
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (struct {
		Observed ObservedState
		Found    bool
	}, error) {
		obs, ok, runErr := api.DescribeParameter(rc, name)
		if runErr != nil {
			if IsNotFound(runErr) {
				return struct {
					Observed ObservedState
					Found    bool
				}{}, nil
			}
			return struct {
				Observed ObservedState
				Found    bool
			}{}, runErr
		}
		if !ok {
			return struct {
				Observed ObservedState
				Found    bool
			}{}, nil
		}
		return struct {
			Observed ObservedState
			Found    bool
		}{Observed: obs, Found: true}, nil
	})
	if err != nil {
		return ObservedState{}, false, err
	}
	return observed.Observed, observed.Found, nil
}

func specFromObserved(observed ObservedState) SSMParameterSpec {
	kmsKeyID := observed.KmsKeyID
	if observed.Type == "SecureString" && kmsKeyID == "alias/aws/ssm" {
		kmsKeyID = ""
	}
	return SSMParameterSpec{
		ParameterName:  observed.ParameterName,
		Type:           observed.Type,
		Value:          observed.Value,
		Description:    observed.Description,
		Tier:           normalizeTier(observed.Tier),
		KmsKeyID:       kmsKeyID,
		AllowedPattern: observed.AllowedPattern,
		DataType:       normalizeDataType(observed.DataType),
		Tags:           drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) SSMParameterOutputs {
	return SSMParameterOutputs{
		ARN:           observed.ARN,
		ParameterName: observed.ParameterName,
		Type:          observed.Type,
		Version:       observed.Version,
		Tier:          normalizeTier(observed.Tier),
		DataType:      normalizeDataType(observed.DataType),
	}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

func applyDefaults(spec SSMParameterSpec) SSMParameterSpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.ParameterName = strings.TrimSpace(spec.ParameterName)
	spec.Type = strings.TrimSpace(spec.Type)
	if spec.Type == "" {
		spec.Type = "String"
	}
	spec.Tier = normalizeTier(spec.Tier)
	spec.DataType = normalizeDataType(spec.DataType)
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec SSMParameterSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.ParameterName) == "" {
		return fmt.Errorf("parameterName is required")
	}
	if spec.Value == "" {
		return fmt.Errorf("value is required")
	}
	switch spec.Type {
	case "String", "StringList", "SecureString":
	default:
		return fmt.Errorf("type must be String, StringList, or SecureString")
	}
	switch spec.Tier {
	case "Standard", "Advanced", "Intelligent-Tiering":
	default:
		return fmt.Errorf("tier must be Standard, Advanced, or Intelligent-Tiering")
	}
	if spec.KmsKeyID != "" && spec.Type != "SecureString" {
		return fmt.Errorf("kmsKeyId is only valid for SecureString parameters")
	}
	switch spec.DataType {
	case "text", "aws:ec2:image", "aws:ssm:integration":
	default:
		return fmt.Errorf("dataType must be text, aws:ec2:image, or aws:ssm:integration")
	}
	return nil
}

// ClearState clears all Virtual Object state for this resource.
// Used by the Orphan deletion policy to release a resource from management.
func (d *SSMParameterDriver) ClearState(ctx restate.ObjectContext) error {
	drivers.ClearAllState(ctx)
	return nil
}
