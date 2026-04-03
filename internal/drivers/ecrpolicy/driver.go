// Package ecrpolicy – driver.go
//
// This file implements the Restate Virtual Object handler for AWS ECR Lifecycle Policy.
// The driver exposes five durable handlers:
//   - Provision: create-or-update the resource and persist state
//   - Import:    adopt an existing AWS resource into Praxis management
//   - Delete:    remove the resource from AWS (managed mode only)
//   - Reconcile: periodic drift check + auto-correction (managed mode)
//   - GetStatus / GetOutputs: read-only shared handlers for status queries
//
// All mutating AWS calls are wrapped in restate.Run for durable execution,
// and reconciliation is self-scheduled via delayed Restate messages.
package ecrpolicy

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

// ECRLifecyclePolicyDriver is the Restate Virtual Object handler for AWS ECR Lifecycle Policy.
// It holds an auth client (for cross-account credential resolution)
// and an API factory (swappable for testing).
type ECRLifecyclePolicyDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) LifecyclePolicyAPI
}

// NewECRLifecyclePolicyDriver creates a ECRLifecyclePolicy driver wired to the given
// auth client. It uses the default AWS SDK client factory.
func NewECRLifecyclePolicyDriver(auth authservice.AuthClient) *ECRLifecyclePolicyDriver {
	return NewECRLifecyclePolicyDriverWithFactory(auth, func(cfg aws.Config) LifecyclePolicyAPI {
		return NewLifecyclePolicyAPI(awsclient.NewECRClient(cfg))
	})
}

// NewECRLifecyclePolicyDriverWithFactory creates a ECRLifecyclePolicy driver with a custom API
// factory, primarily used in tests to inject mock AWS clients.
func NewECRLifecyclePolicyDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) LifecyclePolicyAPI) *ECRLifecyclePolicyDriver {
	if factory == nil {
		factory = func(cfg aws.Config) LifecyclePolicyAPI { return NewLifecyclePolicyAPI(awsclient.NewECRClient(cfg)) }
	}
	return &ECRLifecyclePolicyDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object service name for registration.
func (d *ECRLifecyclePolicyDriver) ServiceName() string { return ServiceName }

// Provision creates or updates a AWS ECR Lifecycle Policy. It validates the spec,
// checks for an existing resource (by ARN or name), detects immutable-field
// conflicts, and either creates a new resource or corrects drift on the
// existing one. State is persisted in Restate K/V after every step.
func (d *ECRLifecyclePolicyDriver) Provision(ctx restate.ObjectContext, spec ECRLifecyclePolicySpec) (ECRLifecyclePolicyOutputs, error) {
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return ECRLifecyclePolicyOutputs{}, restate.TerminalError(err, 400)
	}
	if spec.Region == "" {
		spec.Region = region
	}
	if err := validateProvisionSpec(spec); err != nil {
		return ECRLifecyclePolicyOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[ECRLifecyclePolicyState](ctx, drivers.StateKey)
	if err != nil {
		return ECRLifecyclePolicyOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		created, runErr := api.PutLifecyclePolicy(rc, spec)
		if runErr != nil {
			if IsInvalidParameter(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 400)
			}
			if IsRepositoryNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, runErr
		}
		return created, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return ECRLifecyclePolicyOutputs{}, err
	}
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Import adopts an existing AWS ECR Lifecycle Policy into Praxis management.
// It reads the current configuration from AWS, synthesizes a spec from
// the observed state, and stores it. Default import mode is Observed
// (read-only); users can re-import with --mode managed to enable writes.
func (d *ECRLifecyclePolicyDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (ECRLifecyclePolicyOutputs, error) {
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return ECRLifecyclePolicyOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[ECRLifecyclePolicyState](ctx, drivers.StateKey)
	if err != nil {
		return ECRLifecyclePolicyOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.GetLifecyclePolicy(rc, ref.ResourceID)
	})
	if err != nil {
		if IsNotFound(err) {
			return ECRLifecyclePolicyOutputs{}, restate.TerminalError(fmt.Errorf("import failed: ECR lifecycle policy for repository %s does not exist", ref.ResourceID), 404)
		}
		return ECRLifecyclePolicyOutputs{}, err
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	if spec.Region == "" {
		spec.Region = region
	}
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Mode = drivers.DefaultMode(ref.Mode)
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Delete removes the AWS ECR Lifecycle Policy from AWS. It is blocked for
// resources in Observed mode. The method handles not-found gracefully
// (idempotent delete) and sets the final state to StatusDeleted.
func (d *ECRLifecyclePolicyDriver) Delete(ctx restate.ObjectContext) error {
	state, err := restate.Get[ECRLifecyclePolicyState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete ECR lifecycle policy for %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.RepositoryName), 409)
	}
	name := state.Desired.RepositoryName
	if name == "" {
		name = state.Outputs.RepositoryName
	}
	if name == "" {
		restate.Set(ctx, drivers.StateKey, ECRLifecyclePolicyState{Status: types.StatusDeleted})
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
		deleteErr := api.DeleteLifecyclePolicy(rc, name)
		if deleteErr != nil && !IsNotFound(deleteErr) {
			return restate.Void{}, deleteErr
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}
	restate.Set(ctx, drivers.StateKey, ECRLifecyclePolicyState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-check handler. It re-reads the
// resource from AWS, compares against desired state, and auto-corrects
// drift when in Managed mode. In Observed mode it only reports drift.
// External deletions are detected and flagged as errors.
// The handler self-schedules via a delayed Restate message.
func (d *ECRLifecyclePolicyDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[ECRLifecyclePolicyState](ctx, drivers.StateKey)
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
	if state.Outputs.RepositoryName == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) { return time.Now().UTC().Format(time.RFC3339), nil })
	if err != nil {
		return types.ReconcileResult{}, err
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.GetLifecyclePolicy(rc, state.Outputs.RepositoryName)
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("ECR lifecycle policy for repository %s was deleted externally", state.Outputs.RepositoryName)
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
	drift := HasDrift(state.Desired, observed)
	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}
	if drift && state.Mode == types.ModeManaged {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		_, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.PutLifecyclePolicy(rc, state.Desired)
		})
		if err != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: err.Error()}, nil
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
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

// GetStatus is a shared (read-only) handler that returns the current lifecycle status.
func (d *ECRLifecyclePolicyDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[ECRLifecyclePolicyState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a shared (read-only) handler that returns the provisioned resource outputs.
func (d *ECRLifecyclePolicyDriver) GetOutputs(ctx restate.ObjectSharedContext) (ECRLifecyclePolicyOutputs, error) {
	state, err := restate.Get[ECRLifecyclePolicyState](ctx, drivers.StateKey)
	if err != nil {
		return ECRLifecyclePolicyOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *ECRLifecyclePolicyDriver) scheduleReconcile(ctx restate.ObjectContext, state *ECRLifecyclePolicyState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *ECRLifecyclePolicyDriver) apiForAccount(ctx restate.ObjectContext, account string) (LifecyclePolicyAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("ECRLifecyclePolicyDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve ECR lifecycle policy account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func validateProvisionSpec(spec ECRLifecyclePolicySpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.RepositoryName) == "" {
		return fmt.Errorf("repositoryName is required")
	}
	return validatePolicyJSON(spec.LifecyclePolicyText)
}
