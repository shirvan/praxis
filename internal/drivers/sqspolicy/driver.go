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

type SQSQueuePolicyDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) PolicyAPI
}

func NewSQSQueuePolicyDriver(auth authservice.AuthClient) *SQSQueuePolicyDriver {
	return NewSQSQueuePolicyDriverWithFactory(auth, func(cfg aws.Config) PolicyAPI {
		return NewPolicyAPI(awsclient.NewSQSClient(cfg))
	})
}

func NewSQSQueuePolicyDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) PolicyAPI) *SQSQueuePolicyDriver {
	if factory == nil {
		factory = func(cfg aws.Config) PolicyAPI {
			return NewPolicyAPI(awsclient.NewSQSClient(cfg))
		}
	}
	return &SQSQueuePolicyDriver{auth: auth, apiFactory: factory}
}

func (SQSQueuePolicyDriver) ServiceName() string { return ServiceName }

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

func (d *SQSQueuePolicyDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[SQSQueuePolicyState](ctx, shared.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *SQSQueuePolicyDriver) GetOutputs(ctx restate.ObjectSharedContext) (SQSQueuePolicyOutputs, error) {
	state, err := restate.Get[SQSQueuePolicyState](ctx, shared.StateKey)
	if err != nil {
		return SQSQueuePolicyOutputs{}, err
	}
	return state.Outputs, nil
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
