// Package targetgroup – driver.go
//
// This file implements the Restate Virtual Object handler for AWS ELBv2 Target Group.
// The driver exposes five durable handlers:
//   - Provision: create-or-update the resource and persist state
//   - Import:    adopt an existing AWS resource into Praxis management
//   - Delete:    remove the resource from AWS (managed mode only)
//   - Reconcile: periodic drift check + auto-correction (managed mode)
//   - GetStatus / GetOutputs: read-only shared handlers for status queries
//
// All mutating AWS calls are wrapped in restate.Run for durable execution,
// and reconciliation is self-scheduled via delayed Restate messages.
package targetgroup

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

// TargetGroupDriver is the Restate Virtual Object handler for AWS ELBv2 Target Group.
// It holds an auth client (for cross-account credential resolution)
// and an API factory (swappable for testing).
type TargetGroupDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) TargetGroupAPI
}

// NewTargetGroupDriver creates a TargetGroup driver wired to the given
// auth client. It uses the default AWS SDK client factory.
func NewTargetGroupDriver(auth authservice.AuthClient) *TargetGroupDriver {
	return NewTargetGroupDriverWithFactory(auth, func(cfg aws.Config) TargetGroupAPI {
		return NewTargetGroupAPI(awsclient.NewELBv2Client(cfg))
	})
}

// NewTargetGroupDriverWithFactory creates a TargetGroup driver with a custom API
// factory, primarily used in tests to inject mock AWS clients.
func NewTargetGroupDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) TargetGroupAPI) *TargetGroupDriver {
	if factory == nil {
		factory = func(cfg aws.Config) TargetGroupAPI {
			return NewTargetGroupAPI(awsclient.NewELBv2Client(cfg))
		}
	}
	return &TargetGroupDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object service name for registration.
func (d *TargetGroupDriver) ServiceName() string { return ServiceName }

// Provision creates or updates a AWS ELBv2 Target Group. It validates the spec,
// checks for an existing resource (by ARN or name), detects immutable-field
// conflicts, and either creates a new resource or corrects drift on the
// existing one. State is persisted in Restate K/V after every step.
func (d *TargetGroupDriver) Provision(ctx restate.ObjectContext, spec TargetGroupSpec) (TargetGroupOutputs, error) {
	ctx.Log().Info("provisioning target group", "key", restate.Key(ctx))
	api, _, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return TargetGroupOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	if err := validateSpec(spec); err != nil {
		return TargetGroupOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[TargetGroupState](ctx, drivers.StateKey)
	if err != nil {
		return TargetGroupOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	current, found, err := d.lookupCurrent(ctx, api, state.Outputs.TargetGroupArn, spec.Name)
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return TargetGroupOutputs{}, err
	}
	if found && hasImmutableChange(spec, current) {
		err := fmt.Errorf("target group %q requires replacement because immutable fields changed; delete and re-apply to recreate it", spec.Name)
		state.Observed = current
		state.Outputs = outputsFromObserved(current)
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return TargetGroupOutputs{}, restate.TerminalError(err, 409)
	}

	if !found {
		outputs, runErr := restate.Run(ctx, func(rc restate.RunContext) (TargetGroupOutputs, error) {
			created, createErr := api.CreateTargetGroup(rc, spec)
			if createErr != nil {
				if IsDuplicate(createErr) || IsInvalidConfiguration(createErr) {
					return TargetGroupOutputs{}, restate.TerminalError(createErr, 409)
				}
				if IsTooMany(createErr) {
					return TargetGroupOutputs{}, restate.TerminalError(createErr, 503)
				}
				return TargetGroupOutputs{}, createErr
			}
			return created, nil
		})
		if runErr != nil {
			state.Status = types.StatusError
			state.Error = runErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return TargetGroupOutputs{}, runErr
		}
		current, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, descErr := api.DescribeTargetGroup(rc, outputs.TargetGroupArn)
			if descErr != nil {
				if IsNotFound(descErr) {
					return ObservedState{}, restate.TerminalError(descErr, 404)
				}
				return ObservedState{}, descErr
			}
			return obs, nil
		})
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			state.Outputs = outputs
			restate.Set(ctx, drivers.StateKey, state)
			return TargetGroupOutputs{}, err
		}
	} else {
		if err := d.correctDrift(ctx, api, current.TargetGroupArn, spec, current); err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			state.Observed = current
			state.Outputs = outputsFromObserved(current)
			restate.Set(ctx, drivers.StateKey, state)
			return TargetGroupOutputs{}, err
		}
		current, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, descErr := api.DescribeTargetGroup(rc, current.TargetGroupArn)
			if descErr != nil {
				if IsNotFound(descErr) {
					return ObservedState{}, restate.TerminalError(descErr, 404)
				}
				return ObservedState{}, descErr
			}
			return obs, nil
		})
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return TargetGroupOutputs{}, err
		}
	}

	state.Observed = current
	state.Outputs = outputsFromObserved(current)
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Import adopts an existing AWS ELBv2 Target Group into Praxis management.
// It reads the current configuration from AWS, synthesizes a spec from
// the observed state, and stores it. Default import mode is Observed
// (read-only); users can re-import with --mode managed to enable writes.
func (d *TargetGroupDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (TargetGroupOutputs, error) {
	ctx.Log().Info("importing target group", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return TargetGroupOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[TargetGroupState](ctx, drivers.StateKey)
	if err != nil {
		return TargetGroupOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, descErr := api.DescribeTargetGroup(rc, ref.ResourceID)
		if descErr != nil {
			if IsNotFound(descErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: target group %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, descErr
		}
		return obs, nil
	})
	if err != nil {
		return TargetGroupOutputs{}, err
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

// Delete removes the AWS ELBv2 Target Group from AWS. It is blocked for
// resources in Observed mode. The method handles not-found gracefully
// (idempotent delete) and sets the final state to StatusDeleted.
func (d *TargetGroupDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting target group", "key", restate.Key(ctx))
	state, err := restate.Get[TargetGroupState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete target group %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.TargetGroupArn), 409)
	}
	if state.Outputs.TargetGroupArn == "" {
		restate.Set(ctx, drivers.StateKey, TargetGroupState{Status: types.StatusDeleted})
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
		observed, descErr := api.DescribeTargetGroup(rc, state.Outputs.TargetGroupArn)
		if descErr == nil && len(observed.Targets) > 0 {
			if updateErr := api.UpdateTargets(rc, state.Outputs.TargetGroupArn, nil, observed.Targets); updateErr != nil {
				return restate.Void{}, updateErr
			}
		}
		runErr := api.DeleteTargetGroup(rc, state.Outputs.TargetGroupArn)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsResourceInUse(runErr) {
				return restate.Void{}, restate.TerminalError(fmt.Errorf("target group %s is still referenced by a listener or listener rule", state.Outputs.TargetGroupArn), 409)
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
	restate.Set(ctx, drivers.StateKey, TargetGroupState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-check handler. It re-reads the
// resource from AWS, compares against desired state, and auto-corrects
// drift when in Managed mode. In Observed mode it only reports drift.
// External deletions are detected and flagged as errors.
// The handler self-schedules via a delayed Restate message.
func (d *TargetGroupDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[TargetGroupState](ctx, drivers.StateKey)
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
	if state.Outputs.TargetGroupArn == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) { return time.Now().UTC().Format(time.RFC3339), nil })
	if err != nil {
		return types.ReconcileResult{}, err
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, descErr := api.DescribeTargetGroup(rc, state.Outputs.TargetGroupArn)
		if descErr != nil {
			if IsNotFound(descErr) {
				return ObservedState{}, restate.TerminalError(descErr, 404)
			}
			return ObservedState{}, descErr
		}
		return obs, nil
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("target group %s was deleted externally", state.Outputs.TargetGroupArn)
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
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed)
	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift}, nil
	}
	if drift && state.Mode == types.ModeManaged {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		correctionErr := d.correctDrift(ctx, api, observed.TargetGroupArn, state.Desired, observed)
		if correctionErr != nil {
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
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		return types.ReconcileResult{Drift: true}, nil
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

// GetStatus is a shared (read-only) handler that returns the current lifecycle status.
func (d *TargetGroupDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[TargetGroupState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a shared (read-only) handler that returns the provisioned resource outputs.
func (d *TargetGroupDriver) GetOutputs(ctx restate.ObjectSharedContext) (TargetGroupOutputs, error) {
	state, err := restate.Get[TargetGroupState](ctx, drivers.StateKey)
	if err != nil {
		return TargetGroupOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *TargetGroupDriver) GetInputs(ctx restate.ObjectSharedContext) (TargetGroupSpec, error) {
	state, err := restate.Get[TargetGroupState](ctx, drivers.StateKey)
	if err != nil {
		return TargetGroupSpec{}, err
	}
	return state.Desired, nil
}

func (d *TargetGroupDriver) correctDrift(ctx restate.ObjectContext, api TargetGroupAPI, arn string, desired TargetGroupSpec, observed ObservedState) error {
	if desired.HealthCheck != observed.HealthCheck {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyTargetGroup(rc, arn, desired)
		})
		if err != nil {
			return fmt.Errorf("modify health check: %w", err)
		}
	}
	if desired.DeregistrationDelay != observed.DeregistrationDelay || !stickinessEqual(desired.Stickiness, observed.Stickiness) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateAttributes(rc, arn, desired)
		})
		if err != nil {
			return fmt.Errorf("update attributes: %w", err)
		}
	}
	if !targetsEqual(desired.Targets, observed.Targets) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTargets(rc, arn, desired.Targets, observed.Targets)
		})
		if err != nil {
			return fmt.Errorf("update targets: %w", err)
		}
	}
	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, arn, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}
	return nil
}

func (d *TargetGroupDriver) scheduleReconcile(ctx restate.ObjectContext, state *TargetGroupState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *TargetGroupDriver) apiForAccount(ctx restate.ObjectContext, account string) (TargetGroupAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("TargetGroupDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve target group account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func (d *TargetGroupDriver) lookupCurrent(ctx restate.ObjectContext, api TargetGroupAPI, arn, name string) (ObservedState, bool, error) {
	if arn != "" {
		observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, descErr := api.DescribeTargetGroup(rc, arn)
			if descErr != nil {
				if IsNotFound(descErr) {
					return ObservedState{}, nil
				}
				return ObservedState{}, descErr
			}
			return obs, nil
		})
		if err != nil {
			return ObservedState{}, false, err
		}
		if observed.TargetGroupArn != "" {
			return observed, true, nil
		}
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, descErr := api.DescribeTargetGroup(rc, name)
		if descErr != nil {
			if IsNotFound(descErr) {
				return ObservedState{}, nil
			}
			return ObservedState{}, descErr
		}
		return obs, nil
	})
	if err != nil {
		return ObservedState{}, false, err
	}
	return observed, observed.TargetGroupArn != "", nil
}

func applyDefaults(spec TargetGroupSpec) TargetGroupSpec {
	if spec.TargetType == "" {
		spec.TargetType = "instance"
	}
	spec.HealthCheck = healthCheckWithDefaults(spec.HealthCheck)
	if spec.DeregistrationDelay == 0 {
		spec.DeregistrationDelay = 300
	}
	if spec.Stickiness != nil {
		if spec.Stickiness.Type == "" {
			spec.Stickiness.Type = "lb_cookie"
		}
		if spec.Stickiness.Duration == 0 {
			spec.Stickiness.Duration = 86400
		}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	if spec.Targets == nil {
		spec.Targets = []Target{}
	}
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Region = strings.TrimSpace(spec.Region)
	spec.VpcId = strings.TrimSpace(spec.VpcId)
	return spec
}

func validateSpec(spec TargetGroupSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.Name == "" {
		return fmt.Errorf("name is required")
	}
	if spec.Protocol == "" {
		return fmt.Errorf("protocol is required")
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if spec.TargetType != "lambda" && spec.VpcId == "" {
		return fmt.Errorf("vpcId is required for non-lambda target groups")
	}
	for _, target := range spec.Targets {
		if strings.TrimSpace(target.ID) == "" {
			return fmt.Errorf("target id is required")
		}
	}
	return nil
}

func hasImmutableChange(desired TargetGroupSpec, observed ObservedState) bool {
	return desired.Protocol != observed.Protocol || desired.Port != observed.Port || desired.VpcId != observed.VpcId || desired.TargetType != observed.TargetType || desired.ProtocolVersion != observed.ProtocolVersion
}

func specFromObserved(observed ObservedState) TargetGroupSpec {
	return applyDefaults(TargetGroupSpec{
		Name:                observed.Name,
		Protocol:            observed.Protocol,
		Port:                observed.Port,
		VpcId:               observed.VpcId,
		TargetType:          observed.TargetType,
		ProtocolVersion:     observed.ProtocolVersion,
		HealthCheck:         observed.HealthCheck,
		DeregistrationDelay: observed.DeregistrationDelay,
		Stickiness:          observed.Stickiness,
		Targets:             observed.Targets,
		Tags:                filterPraxisTags(observed.Tags),
	})
}

func outputsFromObserved(observed ObservedState) TargetGroupOutputs {
	return TargetGroupOutputs{TargetGroupArn: observed.TargetGroupArn, TargetGroupName: observed.Name}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}
