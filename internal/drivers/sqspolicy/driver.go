// Package sqspolicy – driver.go
//
// This file implements the Restate Virtual Object handler for AWS SQS Queue Policy.
// The driver exposes five durable handlers:
//   - Provision: create-or-update the resource and persist state
//   - Import:    adopt an existing AWS resource into Praxis management
//   - Delete:    remove the resource from AWS (managed mode only)
//   - Reconcile: periodic drift check + auto-correction (managed mode)
//   - GetStatus / GetOutputs: read-only shared handlers for status queries
//
// All mutating AWS calls are wrapped in restate.Run for durable execution,
// and reconciliation is self-scheduled via delayed Restate messages.
package sqspolicy

import (
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	shared "github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// SQSQueuePolicyDriver is the Restate Virtual Object handler for AWS SQS Queue Policy.
// It holds an auth client (for cross-account credential resolution)
// and an API factory (swappable for testing).
type SQSQueuePolicyDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) PolicyAPI
}

// NewSQSQueuePolicyDriver creates a SQSQueuePolicy driver wired to the given
// auth client. It uses the default AWS SDK client factory.
func NewSQSQueuePolicyDriver(auth authservice.AuthClient) *SQSQueuePolicyDriver {
	return NewSQSQueuePolicyDriverWithFactory(auth, func(cfg aws.Config) PolicyAPI {
		return NewPolicyAPI(awsclient.NewSQSClient(cfg))
	})
}

// NewSQSQueuePolicyDriverWithFactory creates a SQSQueuePolicy driver with a custom API
// factory, primarily used in tests to inject mock AWS clients.
func NewSQSQueuePolicyDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) PolicyAPI) *SQSQueuePolicyDriver {
	if factory == nil {
		factory = func(cfg aws.Config) PolicyAPI {
			return NewPolicyAPI(awsclient.NewSQSClient(cfg))
		}
	}
	return &SQSQueuePolicyDriver{auth: auth, apiFactory: factory}
}

func (SQSQueuePolicyDriver) ServiceName() string { return ServiceName }

// Provision creates or updates a AWS SQS Queue Policy. It validates the spec,
// checks for an existing resource (by ARN or name), detects immutable-field
// conflicts, and either creates a new resource or corrects drift on the
// existing one. State is persisted in Restate K/V after every step.
func (d *SQSQueuePolicyDriver) Provision(ctx restate.ObjectContext, spec SQSQueuePolicySpec) (SQSQueuePolicyOutputs, error) {
	ctx.Log().Info("provisioning SQS queue policy", "key", restate.Key(ctx))
	if spec.Region == "" || spec.QueueName == "" || strings.TrimSpace(spec.Policy) == "" {
		return SQSQueuePolicyOutputs{}, restate.TerminalError(fmt.Errorf("region, queueName, and policy are required"), 400)
	}

	api, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return SQSQueuePolicyOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[SQSQueuePolicyState](ctx, shared.StateKey)
	if err != nil {
		return SQSQueuePolicyOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	queueURL := state.Outputs.QueueUrl
	if queueURL == "" {
		resolvedURL, resolveErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, runErr := api.GetQueueUrl(rc, spec.QueueName)
			if runErr != nil {
				if IsNotFound(runErr) {
					return "", restate.TerminalError(fmt.Errorf("queue %q not found", spec.QueueName), 404)
				}
				return "", shared.ClassifyAPIError(runErr, spec.Account, ServiceName)
			}
			return id, nil
		})
		if resolveErr != nil {
			state.Status = types.StatusError
			state.Error = resolveErr.Error()
			restate.Set(ctx, shared.StateKey, state)
			return SQSQueuePolicyOutputs{}, resolveErr
		}
		queueURL = resolvedURL
	}

	if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.SetQueuePolicy(rc, queueURL, spec.Policy)
		if runErr != nil {
			if IsInvalidInput(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
			}
			return restate.Void{}, shared.ClassifyAPIError(runErr, spec.Account, ServiceName)
		}
		return restate.Void{}, nil
	}); err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, shared.StateKey, state)
		return SQSQueuePolicyOutputs{}, err
	}

	observed, obsErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.GetQueuePolicy(rc, queueURL)
	})
	if obsErr != nil {
		observed = ObservedState{QueueUrl: queueURL}
	}

	outputs := SQSQueuePolicyOutputs{QueueUrl: queueURL, QueueArn: observed.QueueArn, QueueName: spec.QueueName}
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, shared.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Import adopts an existing AWS SQS Queue Policy into Praxis management.
// It reads the current configuration from AWS, synthesizes a spec from
// the observed state, and stores it. Default import mode is Observed
// (read-only); users can re-import with --mode managed to enable writes.
func (d *SQSQueuePolicyDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (SQSQueuePolicyOutputs, error) {
	ctx.Log().Info("importing SQS queue policy", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return SQSQueuePolicyOutputs{}, restate.TerminalError(err, 400)
	}

	queueName := ref.ResourceID
	if strings.HasPrefix(queueName, "http://") || strings.HasPrefix(queueName, "https://") {
		parts := strings.Split(queueName, "/")
		queueName = parts[len(parts)-1]
	}

	queueURL, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		id, runErr := api.GetQueueUrl(rc, queueName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return "", restate.TerminalError(fmt.Errorf("queue %q not found", queueName), 404)
			}
			return "", shared.ClassifyAPIError(runErr, ref.Account, ServiceName)
		}
		return id, nil
	})
	if err != nil {
		return SQSQueuePolicyOutputs{}, err
	}

	observed, obsErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		o, runErr := api.GetQueuePolicy(rc, queueURL)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("queue %q not found", queueName), 404)
			}
			return ObservedState{}, shared.ClassifyAPIError(runErr, ref.Account, ServiceName)
		}
		return o, nil
	})
	if obsErr != nil {
		return SQSQueuePolicyOutputs{}, obsErr
	}
	if strings.TrimSpace(observed.Policy) == "" {
		return SQSQueuePolicyOutputs{}, restate.TerminalError(fmt.Errorf("queue %q has no policy to import", queueName), 404)
	}

	spec := SQSQueuePolicySpec{Account: ref.Account, Region: regionFromQueueARN(observed.QueueArn), QueueName: queueName, Policy: observed.Policy}
	outputs := SQSQueuePolicyOutputs{QueueUrl: queueURL, QueueArn: observed.QueueArn, QueueName: queueName}
	mode := defaultImportMode(ref.Mode)

	state, err := restate.Get[SQSQueuePolicyState](ctx, shared.StateKey)
	if err != nil {
		return SQSQueuePolicyOutputs{}, err
	}
	state.Generation++
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Mode = mode
	state.Error = ""
	restate.Set(ctx, shared.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Delete removes the AWS SQS Queue Policy from AWS. It is blocked for
// resources in Observed mode. The method handles not-found gracefully
// (idempotent delete) and sets the final state to StatusDeleted.
func (d *SQSQueuePolicyDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting SQS queue policy", "key", restate.Key(ctx))
	state, err := restate.Get[SQSQueuePolicyState](ctx, shared.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete queue policy %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.QueueUrl), 409)
	}
	if state.Outputs.QueueUrl == "" {
		restate.Set(ctx, shared.StateKey, SQSQueuePolicyState{Status: types.StatusDeleted})
		return nil
	}

	api, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, shared.StateKey, state)

	if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.RemoveQueuePolicy(rc, state.Outputs.QueueUrl)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsInvalidInput(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
			}
			return restate.Void{}, shared.ClassifyAPIError(runErr, state.Desired.Account, ServiceName)
		}
		return restate.Void{}, nil
	}); err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, shared.StateKey, state)
		return err
	}

	restate.Set(ctx, shared.StateKey, SQSQueuePolicyState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-check handler. It re-reads the
// resource from AWS, compares against desired state, and auto-corrects
// drift when in Managed mode. In Observed mode it only reports drift.
// External deletions are detected and flagged as errors.
// The handler self-schedules via a delayed Restate message.
func (d *SQSQueuePolicyDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[SQSQueuePolicyState](ctx, shared.StateKey)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	api, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return types.ReconcileResult{}, restate.TerminalError(err, 400)
	}

	state.ReconcileScheduled = false
	if state.Status != types.StatusReady && state.Status != types.StatusError {
		restate.Set(ctx, shared.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	if state.Outputs.QueueUrl == "" {
		restate.Set(ctx, shared.StateKey, state)
		return types.ReconcileResult{}, nil
	}

	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return types.ReconcileResult{}, err
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		o, runErr := api.GetQueuePolicy(rc, state.Outputs.QueueUrl)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, shared.ClassifyAPIError(runErr, state.Desired.Account, ServiceName)
		}
		return o, nil
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("queue policy target %s was deleted externally", state.Outputs.QueueUrl)
			state.LastReconcile = now
			restate.Set(ctx, shared.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			shared.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventExternalDelete, state.Error)
			return types.ReconcileResult{Error: state.Error}, nil
		}
		state.LastReconcile = now
		restate.Set(ctx, shared.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}

	state.Observed = observed
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed)

	if state.Status == types.StatusError {
		restate.Set(ctx, shared.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}

	if drift && state.Mode == types.ModeManaged {
		ctx.Log().Info("drift detected, correcting", "queueUrl", state.Outputs.QueueUrl)
		shared.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.SetQueuePolicy(rc, state.Outputs.QueueUrl, state.Desired.Policy)
			if runErr != nil {
				if IsInvalidInput(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				return restate.Void{}, shared.ClassifyAPIError(runErr, state.Desired.Account, ServiceName)
			}
			return restate.Void{}, nil
		}); err != nil {
			restate.Set(ctx, shared.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: err.Error()}, nil
		}
		restate.Set(ctx, shared.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		shared.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}

	if drift && state.Mode == types.ModeObserved {
		ctx.Log().Info("drift detected (observed mode, not correcting)", "queueUrl", state.Outputs.QueueUrl)
		restate.Set(ctx, shared.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		shared.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	restate.Set(ctx, shared.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

// GetStatus is a shared (read-only) handler that returns the current lifecycle status.
func (d *SQSQueuePolicyDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[SQSQueuePolicyState](ctx, shared.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a shared (read-only) handler that returns the provisioned resource outputs.
func (d *SQSQueuePolicyDriver) GetOutputs(ctx restate.ObjectSharedContext) (SQSQueuePolicyOutputs, error) {
	state, err := restate.Get[SQSQueuePolicyState](ctx, shared.StateKey)
	if err != nil {
		return SQSQueuePolicyOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *SQSQueuePolicyDriver) GetInputs(ctx restate.ObjectSharedContext) (SQSQueuePolicySpec, error) {
	state, err := restate.Get[SQSQueuePolicyState](ctx, shared.StateKey)
	if err != nil {
		return SQSQueuePolicySpec{}, err
	}
	return state.Desired, nil
}

func (d *SQSQueuePolicyDriver) scheduleReconcile(ctx restate.ObjectContext, state *SQSQueuePolicyState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, shared.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(shared.ReconcileInterval))
}

func (d *SQSQueuePolicyDriver) apiForAccount(ctx restate.ObjectContext, account string) (PolicyAPI, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, fmt.Errorf("SQSQueuePolicyDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve SQS account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), nil
}

func regionFromQueueARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 4 {
		return parts[3]
	}
	return ""
}

func defaultImportMode(m types.Mode) types.Mode {
	if m == "" {
		return types.ModeObserved
	}
	return m
}

// ClearState clears all Virtual Object state for this resource.
// Used by the Orphan deletion policy to release a resource from management.
func (d *SQSQueuePolicyDriver) ClearState(ctx restate.ObjectContext) error {
	shared.ClearAllState(ctx)
	return nil

}
