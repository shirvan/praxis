package sqs

import (
	"encoding/json"
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

type SQSQueueDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) QueueAPI
}

func NewSQSQueueDriver(auth authservice.AuthClient) *SQSQueueDriver {
	return NewSQSQueueDriverWithFactory(auth, func(cfg aws.Config) QueueAPI {
		return NewQueueAPI(awsclient.NewSQSClient(cfg))
	})
}

func NewSQSQueueDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) QueueAPI) *SQSQueueDriver {
	if factory == nil {
		factory = func(cfg aws.Config) QueueAPI {
			return NewQueueAPI(awsclient.NewSQSClient(cfg))
		}
	}
	return &SQSQueueDriver{auth: auth, apiFactory: factory}
}

func (SQSQueueDriver) ServiceName() string { return ServiceName }

func (d *SQSQueueDriver) Provision(ctx restate.ObjectContext, spec SQSQueueSpec) (SQSQueueOutputs, error) {
	ctx.Log().Info("provisioning SQS queue", "key", restate.Key(ctx))
	spec = applyDefaults(spec)
	if err := validateSpec(spec); err != nil {
		return SQSQueueOutputs{}, restate.TerminalError(err, 400)
	}

	api, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return SQSQueueOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[SQSQueueState](ctx, shared.StateKey)
	if err != nil {
		return SQSQueueOutputs{}, err
	}

	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	queueURL := state.Outputs.QueueUrl
	if queueURL != "" {
		obs, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			o, runErr := api.GetQueueAttributes(rc, queueURL)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, nil
				}
				return ObservedState{}, runErr
			}
			return o, nil
		})
		if descErr != nil {
			state.Status = types.StatusError
			state.Error = descErr.Error()
			restate.Set(ctx, shared.StateKey, state)
			return SQSQueueOutputs{}, descErr
		}
		if obs.QueueUrl == "" {
			queueURL = ""
		}
	}

	if queueURL == "" && spec.ManagedKey != "" {
		conflictURL, conflictErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, spec.ManagedKey)
		})
		if conflictErr != nil {
			state.Status = types.StatusError
			state.Error = conflictErr.Error()
			restate.Set(ctx, shared.StateKey, state)
			return SQSQueueOutputs{}, conflictErr
		}
		if conflictURL != "" {
			conflict := formatManagedKeyConflict(spec.ManagedKey, conflictURL)
			state.Status = types.StatusError
			state.Error = conflict.Error()
			restate.Set(ctx, shared.StateKey, state)
			return SQSQueueOutputs{}, restate.TerminalError(conflict, 409)
		}
	}

	if queueURL == "" {
		createdURL, createErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, runErr := api.CreateQueue(rc, spec)
			if runErr != nil {
				if IsInvalidInput(runErr) {
					return "", restate.TerminalError(runErr, 400)
				}
				if IsAlreadyExists(runErr) {
					return "", restate.TerminalError(fmt.Errorf("queue %q already exists with different attributes", spec.QueueName), 409)
				}
				if IsConflict(runErr) {
					return "", runErr
				}
				return "", shared.ClassifyAPIError(runErr, spec.Account, ServiceName)
			}
			return id, nil
		})
		if createErr != nil {
			state.Status = types.StatusError
			state.Error = createErr.Error()
			restate.Set(ctx, shared.StateKey, state)
			return SQSQueueOutputs{}, createErr
		}
		queueURL = createdURL
	} else {
		if err := d.convergeQueue(ctx, api, queueURL, spec, state.Observed); err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, shared.StateKey, state)
			return SQSQueueOutputs{}, err
		}
	}

	if _, tagErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.UpdateTags(rc, queueURL, mergeTags(spec.Tags, map[string]string{"praxis:managed-key": restate.Key(ctx)}))
	}); tagErr != nil {
		state.Status = types.StatusError
		state.Error = tagErr.Error()
		restate.Set(ctx, shared.StateKey, state)
		return SQSQueueOutputs{}, tagErr
	}

	observed, obsErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.GetQueueAttributes(rc, queueURL)
	})
	if obsErr != nil {
		observed = ObservedState{QueueUrl: queueURL, QueueName: spec.QueueName}
	}

	outputs := SQSQueueOutputs{QueueUrl: queueURL, QueueArn: observed.QueueArn, QueueName: spec.QueueName}
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, shared.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

func (d *SQSQueueDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (SQSQueueOutputs, error) {
	ctx.Log().Info("importing SQS queue", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return SQSQueueOutputs{}, restate.TerminalError(err, 400)
	}

	queueURL := ref.ResourceID
	if !strings.HasPrefix(queueURL, "http://") && !strings.HasPrefix(queueURL, "https://") {
		resolvedURL, resolveErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, runErr := api.GetQueueUrl(rc, ref.ResourceID)
			if runErr != nil {
				if IsNotFound(runErr) {
					return "", restate.TerminalError(fmt.Errorf("queue %q not found", ref.ResourceID), 404)
				}
				return "", shared.ClassifyAPIError(runErr, ref.Account, ServiceName)
			}
			return id, nil
		})
		if resolveErr != nil {
			return SQSQueueOutputs{}, resolveErr
		}
		queueURL = resolvedURL
	}

	observed, obsErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		o, runErr := api.GetQueueAttributes(rc, queueURL)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("queue %q not found", ref.ResourceID), 404)
			}
			return ObservedState{}, shared.ClassifyAPIError(runErr, ref.Account, ServiceName)
		}
		return o, nil
	})
	if obsErr != nil {
		return SQSQueueOutputs{}, obsErr
	}

	spec := specFromObserved(observed, ref)
	outputs := SQSQueueOutputs{QueueUrl: observed.QueueUrl, QueueArn: observed.QueueArn, QueueName: observed.QueueName}
	mode := defaultImportMode(ref.Mode)

	state, err := restate.Get[SQSQueueState](ctx, shared.StateKey)
	if err != nil {
		return SQSQueueOutputs{}, err
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

func (d *SQSQueueDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting SQS queue", "key", restate.Key(ctx))
	state, err := restate.Get[SQSQueueState](ctx, shared.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete queue %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.QueueUrl), 409)
	}
	if state.Outputs.QueueUrl == "" {
		restate.Set(ctx, shared.StateKey, SQSQueueState{Status: types.StatusDeleted})
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
		runErr := api.DeleteQueue(rc, state.Outputs.QueueUrl)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsConflict(runErr) {
				return restate.Void{}, runErr
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

	restate.Set(ctx, shared.StateKey, SQSQueueState{Status: types.StatusDeleted})
	return nil
}

func (d *SQSQueueDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[SQSQueueState](ctx, shared.StateKey)
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
		o, runErr := api.GetQueueAttributes(rc, state.Outputs.QueueUrl)
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
			state.Error = fmt.Sprintf("queue %s was deleted externally", state.Outputs.QueueUrl)
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
		if correctionErr := d.convergeQueue(ctx, api, state.Outputs.QueueUrl, state.Desired, observed); correctionErr != nil {
			restate.Set(ctx, shared.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		if !tagsMatch(state.Desired.Tags, observed.Tags) {
			if _, tagErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.UpdateTags(rc, state.Outputs.QueueUrl, mergeTags(state.Desired.Tags, map[string]string{"praxis:managed-key": restate.Key(ctx)}))
			}); tagErr != nil {
				restate.Set(ctx, shared.StateKey, state)
				d.scheduleReconcile(ctx, &state)
				return types.ReconcileResult{Drift: true, Correcting: true, Error: tagErr.Error()}, nil
			}
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

func (d *SQSQueueDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[SQSQueueState](ctx, shared.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *SQSQueueDriver) GetOutputs(ctx restate.ObjectSharedContext) (SQSQueueOutputs, error) {
	state, err := restate.Get[SQSQueueState](ctx, shared.StateKey)
	if err != nil {
		return SQSQueueOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *SQSQueueDriver) convergeQueue(ctx restate.ObjectContext, api QueueAPI, queueURL string, desired SQSQueueSpec, observed ObservedState) error {
	attrs := map[string]string{
		"VisibilityTimeout":             fmt.Sprintf("%d", desired.VisibilityTimeout),
		"MessageRetentionPeriod":        fmt.Sprintf("%d", desired.MessageRetentionPeriod),
		"MaximumMessageSize":            fmt.Sprintf("%d", desired.MaximumMessageSize),
		"DelaySeconds":                  fmt.Sprintf("%d", desired.DelaySeconds),
		"ReceiveMessageWaitTimeSeconds": fmt.Sprintf("%d", desired.ReceiveMessageWaitTimeSeconds),
	}
	if desired.KmsMasterKeyId != "" {
		attrs["KmsMasterKeyId"] = desired.KmsMasterKeyId
		attrs["KmsDataKeyReusePeriodSeconds"] = fmt.Sprintf("%d", desired.KmsDataKeyReusePeriodSeconds)
		attrs["SqsManagedSseEnabled"] = "false"
	} else {
		attrs["SqsManagedSseEnabled"] = fmt.Sprintf("%t", desired.SqsManagedSseEnabled)
		if observed.KmsMasterKeyId != "" {
			attrs["KmsMasterKeyId"] = ""
		}
	}
	if desired.RedrivePolicy != nil {
		payload, err := jsonMarshal(desired.RedrivePolicy)
		if err != nil {
			return err
		}
		attrs["RedrivePolicy"] = payload
	} else {
		attrs["RedrivePolicy"] = ""
	}
	if desired.FifoQueue {
		attrs["ContentBasedDeduplication"] = fmt.Sprintf("%t", desired.ContentBasedDeduplication)
		if desired.DeduplicationScope != "" {
			attrs["DeduplicationScope"] = desired.DeduplicationScope
		}
		if desired.FifoThroughputLimit != "" {
			attrs["FifoThroughputLimit"] = desired.FifoThroughputLimit
		}
	}
	if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.SetQueueAttributes(rc, queueURL, attrs)
		if runErr != nil {
			if IsInvalidInput(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
			}
			return restate.Void{}, shared.ClassifyAPIError(runErr, desired.Account, ServiceName)
		}
		return restate.Void{}, nil
	}); err != nil {
		return fmt.Errorf("set queue attributes: %w", err)
	}
	return nil
}

func (d *SQSQueueDriver) scheduleReconcile(ctx restate.ObjectContext, state *SQSQueueState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, shared.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(shared.ReconcileInterval))
}

func (d *SQSQueueDriver) apiForAccount(ctx restate.ObjectContext, account string) (QueueAPI, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, fmt.Errorf("SQSQueueDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve SQS account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), nil
}

func specFromObserved(obs ObservedState, ref types.ImportRef) SQSQueueSpec {
	return SQSQueueSpec{
		Account:                       ref.Account,
		Region:                        regionFromQueueARN(obs.QueueArn),
		QueueName:                     obs.QueueName,
		FifoQueue:                     obs.FifoQueue,
		VisibilityTimeout:             obs.VisibilityTimeout,
		MessageRetentionPeriod:        obs.MessageRetentionPeriod,
		MaximumMessageSize:            obs.MaximumMessageSize,
		DelaySeconds:                  obs.DelaySeconds,
		ReceiveMessageWaitTimeSeconds: obs.ReceiveMessageWaitTimeSeconds,
		RedrivePolicy:                 obs.RedrivePolicy,
		SqsManagedSseEnabled:          obs.SqsManagedSseEnabled,
		KmsMasterKeyId:                obs.KmsMasterKeyId,
		KmsDataKeyReusePeriodSeconds:  obs.KmsDataKeyReusePeriodSeconds,
		ContentBasedDeduplication:     obs.ContentBasedDeduplication,
		DeduplicationScope:            obs.DeduplicationScope,
		FifoThroughputLimit:           obs.FifoThroughputLimit,
		Tags:                          filterPraxisTags(obs.Tags),
	}
}

func defaultImportMode(m types.Mode) types.Mode {
	if m == "" {
		return types.ModeObserved
	}
	return m
}

func applyDefaults(spec SQSQueueSpec) SQSQueueSpec {
	if spec.MessageRetentionPeriod == 0 {
		spec.MessageRetentionPeriod = 345600
	}
	if spec.MaximumMessageSize == 0 {
		spec.MaximumMessageSize = 262144
	}
	if spec.VisibilityTimeout == 0 && !spec.FifoQueue {
		spec.VisibilityTimeout = 30
	}
	if spec.KmsMasterKeyId != "" && spec.KmsDataKeyReusePeriodSeconds == 0 {
		spec.KmsDataKeyReusePeriodSeconds = 300
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	return spec
}

func validateSpec(spec SQSQueueSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.QueueName == "" {
		return fmt.Errorf("queueName is required")
	}
	if spec.FifoQueue && !strings.HasSuffix(spec.QueueName, ".fifo") {
		return fmt.Errorf("FIFO queues must have a name ending with .fifo")
	}
	if !spec.FifoQueue && strings.HasSuffix(spec.QueueName, ".fifo") {
		return fmt.Errorf("queueName ending with .fifo requires fifoQueue=true")
	}
	if spec.KmsMasterKeyId != "" && spec.SqsManagedSseEnabled {
		return fmt.Errorf("kmsMasterKeyId and sqsManagedSseEnabled are mutually exclusive")
	}
	return nil
}

func regionFromQueueARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 4 {
		return parts[3]
	}
	return ""
}

func jsonMarshal(policy *RedrivePolicy) (string, error) {
	payload, err := json.Marshal(policy)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}
