// Package listener – driver.go
//
// This file implements the Restate Virtual Object handler for AWS ELBv2 Listener.
// The driver exposes five durable handlers:
//   - Provision: create-or-update the resource and persist state
//   - Import:    adopt an existing AWS resource into Praxis management
//   - Delete:    remove the resource from AWS (managed mode only)
//   - Reconcile: periodic drift check + auto-correction (managed mode)
//   - GetStatus / GetOutputs: read-only shared handlers for status queries
//
// All mutating AWS calls are wrapped in restate.Run for durable execution,
// and reconciliation is self-scheduled via delayed Restate messages.
package listener

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

// ListenerDriver is the Restate Virtual Object handler for AWS ELBv2 Listener.
// It holds an auth client (for cross-account credential resolution)
// and an API factory (swappable for testing).
type ListenerDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) ListenerAPI
}

// NewListenerDriver creates a Listener driver wired to the given
// auth client. It uses the default AWS SDK client factory.
func NewListenerDriver(auth authservice.AuthClient) *ListenerDriver {
	return NewListenerDriverWithFactory(auth, func(cfg aws.Config) ListenerAPI {
		return NewListenerAPI(awsclient.NewELBv2Client(cfg))
	})
}

// NewListenerDriverWithFactory creates a Listener driver with a custom API
// factory, primarily used in tests to inject mock AWS clients.
func NewListenerDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) ListenerAPI) *ListenerDriver {
	if factory == nil {
		factory = func(cfg aws.Config) ListenerAPI {
			return NewListenerAPI(awsclient.NewELBv2Client(cfg))
		}
	}
	return &ListenerDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object service name for registration.
func (d *ListenerDriver) ServiceName() string { return ServiceName }

// Provision creates or updates a AWS ELBv2 Listener. It validates the spec,
// checks for an existing resource (by ARN or name), detects immutable-field
// conflicts, and either creates a new resource or corrects drift on the
// existing one. State is persisted in Restate K/V after every step.
func (d *ListenerDriver) Provision(ctx restate.ObjectContext, spec ListenerSpec) (ListenerOutputs, error) {
	ctx.Log().Info("provisioning listener", "key", restate.Key(ctx))
	api, _, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return ListenerOutputs{}, restate.TerminalError(err, 400)
	}
	if err := validateSpec(spec); err != nil {
		return ListenerOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[ListenerState](ctx, drivers.StateKey)
	if err != nil {
		return ListenerOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	if state.Outputs.ListenerArn != "" {
		current, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, e := api.DescribeListener(rc, state.Outputs.ListenerArn)
			if e != nil {
				if IsNotFound(e) {
					return ObservedState{}, nil
				}
				return ObservedState{}, e
			}
			return obs, nil
		})
		if descErr != nil {
			state.Status = types.StatusError
			state.Error = descErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return ListenerOutputs{}, descErr
		}
		if current.ListenerArn != "" {
			if hasImmutableChange(spec, current) {
				err := fmt.Errorf("listener %q requires replacement because immutable field loadBalancerArn changed; delete and re-apply", state.Outputs.ListenerArn)
				state.Observed = current
				state.Status = types.StatusError
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return ListenerOutputs{}, restate.TerminalError(err, 409)
			}
			if err := d.correctDrift(ctx, api, current.ListenerArn, spec, current); err != nil {
				state.Status = types.StatusError
				state.Error = err.Error()
				state.Observed = current
				state.Outputs = outputsFromObserved(current)
				restate.Set(ctx, drivers.StateKey, state)
				return ListenerOutputs{}, err
			}
			refreshed, refreshErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
				return api.DescribeListener(rc, current.ListenerArn)
			})
			if refreshErr != nil {
				state.Status = types.StatusError
				state.Error = refreshErr.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return ListenerOutputs{}, refreshErr
			}
			state.Observed = refreshed
			state.Outputs = outputsFromObserved(refreshed)
			state.Status = types.StatusReady
			state.Error = ""
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return state.Outputs, nil
		}
	}

	arn, runErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		a, createErr := api.CreateListener(rc, spec)
		if createErr != nil {
			if IsDuplicate(createErr) {
				return "", restate.TerminalError(createErr, 409)
			}
			if IsTooMany(createErr) {
				return "", restate.TerminalError(createErr, 503)
			}
			if IsTargetGroupNotFound(createErr) || IsCertificateNotFound(createErr) || IsInvalidConfig(createErr) {
				return "", restate.TerminalError(createErr, 400)
			}
			return "", createErr
		}
		return a, nil
	})
	if runErr != nil {
		state.Status = types.StatusError
		state.Error = runErr.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return ListenerOutputs{}, runErr
	}

	observed, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.DescribeListener(rc, arn)
	})
	if descErr != nil {
		state.Status = types.StatusError
		state.Error = descErr.Error()
		state.Outputs = ListenerOutputs{ListenerArn: arn, Port: spec.Port, Protocol: spec.Protocol}
		restate.Set(ctx, drivers.StateKey, state)
		return ListenerOutputs{}, descErr
	}

	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Import adopts an existing AWS ELBv2 Listener into Praxis management.
// It reads the current configuration from AWS, synthesizes a spec from
// the observed state, and stores it. Default import mode is Observed
// (read-only); users can re-import with --mode managed to enable writes.
func (d *ListenerDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (ListenerOutputs, error) {
	ctx.Log().Info("importing listener", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return ListenerOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[ListenerState](ctx, drivers.StateKey)
	if err != nil {
		return ListenerOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, descErr := api.DescribeListener(rc, ref.ResourceID)
		if descErr != nil {
			if IsNotFound(descErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: listener %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, descErr
		}
		return obs, nil
	})
	if err != nil {
		return ListenerOutputs{}, err
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	_ = region
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

// Delete removes the AWS ELBv2 Listener from AWS. It is blocked for
// resources in Observed mode. The method handles not-found gracefully
// (idempotent delete) and sets the final state to StatusDeleted.
func (d *ListenerDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting listener", "key", restate.Key(ctx))
	state, err := restate.Get[ListenerState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete listener %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.ListenerArn), 409)
	}
	if state.Outputs.ListenerArn == "" {
		restate.Set(ctx, drivers.StateKey, ListenerState{Status: types.StatusDeleted})
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
		runErr := api.DeleteListener(rc, state.Outputs.ListenerArn)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsInvalidConfig(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
			}
			if drivers.IsAccessDenied(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 403)
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
	restate.Set(ctx, drivers.StateKey, ListenerState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-check handler. It re-reads the
// resource from AWS, compares against desired state, and auto-corrects
// drift when in Managed mode. In Observed mode it only reports drift.
// External deletions are detected and flagged as errors.
// The handler self-schedules via a delayed Restate message.
func (d *ListenerDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[ListenerState](ctx, drivers.StateKey)
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
	if state.Outputs.ListenerArn == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) { return time.Now().UTC().Format(time.RFC3339), nil })
	if err != nil {
		return types.ReconcileResult{}, err
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, descErr := api.DescribeListener(rc, state.Outputs.ListenerArn)
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
			state.Error = fmt.Sprintf("listener %s was deleted externally", state.Outputs.ListenerArn)
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
		correctionErr := d.correctDrift(ctx, api, observed.ListenerArn, state.Desired, observed)
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
func (d *ListenerDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[ListenerState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a shared (read-only) handler that returns the provisioned resource outputs.
func (d *ListenerDriver) GetOutputs(ctx restate.ObjectSharedContext) (ListenerOutputs, error) {
	state, err := restate.Get[ListenerState](ctx, drivers.StateKey)
	if err != nil {
		return ListenerOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *ListenerDriver) GetInputs(ctx restate.ObjectSharedContext) (ListenerSpec, error) {
	state, err := restate.Get[ListenerState](ctx, drivers.StateKey)
	if err != nil {
		return ListenerSpec{}, err
	}
	return state.Desired, nil
}

func (d *ListenerDriver) correctDrift(ctx restate.ObjectContext, api ListenerAPI, arn string, desired ListenerSpec, observed ObservedState) error {
	needsModify := desired.Port != observed.Port ||
		!strings.EqualFold(desired.Protocol, observed.Protocol) ||
		effectiveSslPolicy(desired.SslPolicy, desired.Protocol) != observed.SslPolicy ||
		desired.CertificateArn != observed.CertificateArn ||
		desired.AlpnPolicy != observed.AlpnPolicy ||
		!actionsEqual(desired.DefaultActions, observed.DefaultActions)

	if needsModify {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyListener(rc, arn, desired)
		})
		if err != nil {
			return fmt.Errorf("modify listener: %w", err)
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, arn, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}
	return nil
}

func (d *ListenerDriver) scheduleReconcile(ctx restate.ObjectContext, state *ListenerState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *ListenerDriver) apiForAccount(ctx restate.ObjectContext, account string) (ListenerAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("ListenerDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve listener account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func validateSpec(spec ListenerSpec) error {
	if spec.LoadBalancerArn == "" {
		return fmt.Errorf("loadBalancerArn is required")
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if spec.Protocol == "" {
		return fmt.Errorf("protocol is required")
	}
	if requiresSSL(spec.Protocol) && spec.CertificateArn == "" {
		return fmt.Errorf("certificateArn is required for %s listeners", spec.Protocol)
	}
	if len(spec.DefaultActions) == 0 {
		return fmt.Errorf("at least one default action is required")
	}
	return nil
}

func hasImmutableChange(desired ListenerSpec, observed ObservedState) bool {
	return desired.LoadBalancerArn != observed.LoadBalancerArn
}

func specFromObserved(observed ObservedState) ListenerSpec {
	return ListenerSpec{
		LoadBalancerArn: observed.LoadBalancerArn,
		Port:            observed.Port,
		Protocol:        observed.Protocol,
		SslPolicy:       observed.SslPolicy,
		CertificateArn:  observed.CertificateArn,
		AlpnPolicy:      observed.AlpnPolicy,
		DefaultActions:  observed.DefaultActions,
		Tags:            drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) ListenerOutputs {
	return ListenerOutputs{
		ListenerArn: observed.ListenerArn,
		Port:        observed.Port,
		Protocol:    observed.Protocol,
	}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}
