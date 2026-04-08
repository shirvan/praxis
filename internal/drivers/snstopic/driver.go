// Package snstopic – driver.go
//
// This file implements the Restate Virtual Object handler for AWS SNS Topic.
// The driver exposes five durable handlers:
//   - Provision: create-or-update the resource and persist state
//   - Import:    adopt an existing AWS resource into Praxis management
//   - Delete:    remove the resource from AWS (managed mode only)
//   - Reconcile: periodic drift check + auto-correction (managed mode)
//   - GetStatus / GetOutputs: read-only shared handlers for status queries
//
// All mutating AWS calls are wrapped in restate.Run for durable execution,
// and reconciliation is self-scheduled via delayed Restate messages.
package snstopic

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

// SNSTopicDriver is the Restate Virtual Object driver for SNS topics.
type SNSTopicDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) TopicAPI
}

// NewSNSTopicDriver returns a driver configured with real AWS credentials.
func NewSNSTopicDriver(auth authservice.AuthClient) *SNSTopicDriver {
	return NewSNSTopicDriverWithFactory(auth, func(cfg aws.Config) TopicAPI {
		return NewTopicAPI(awsclient.NewSNSClient(cfg))
	})
}

// NewSNSTopicDriverWithFactory returns a driver with an injectable API factory (for tests).
func NewSNSTopicDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) TopicAPI) *SNSTopicDriver {
	if factory == nil {
		factory = func(cfg aws.Config) TopicAPI {
			return NewTopicAPI(awsclient.NewSNSClient(cfg))
		}
	}
	return &SNSTopicDriver{auth: auth, apiFactory: factory}
}

func (SNSTopicDriver) ServiceName() string { return ServiceName }

// Provision creates or converges an SNS topic.
func (d *SNSTopicDriver) Provision(ctx restate.ObjectContext, spec SNSTopicSpec) (SNSTopicOutputs, error) {
	ctx.Log().Info("provisioning SNS topic", "key", restate.Key(ctx))

	if spec.FifoTopic && !strings.HasSuffix(spec.TopicName, ".fifo") {
		return SNSTopicOutputs{}, restate.TerminalError(
			fmt.Errorf("FIFO topics must have a name ending with .fifo"), 400)
	}

	api, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return SNSTopicOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[SNSTopicState](ctx, drivers.StateKey)
	if err != nil {
		return SNSTopicOutputs{}, err
	}

	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	topicArn := state.Outputs.TopicArn

	// If we already have an ARN, verify the topic still exists.
	if topicArn != "" {
		obs, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			o, runErr := api.GetTopicAttributes(rc, topicArn)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, nil // topic gone; recreate
				}
				return ObservedState{}, runErr
			}
			return o, nil
		})
		if descErr != nil {
			state.Status = types.StatusError
			state.Error = descErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return SNSTopicOutputs{}, descErr
		}
		if obs.TopicArn == "" {
			topicArn = "" // topic was deleted externally; recreate
		}
	}

	if topicArn == "" {
		// Create the topic (SNS CreateTopic is idempotent by name).
		arn, createErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, runErr := api.CreateTopic(rc, spec)
			if runErr != nil {
				if IsInvalidParameter(runErr) {
					return "", restate.TerminalError(runErr, 400)
				}
				if isAuthError(runErr) {
					return "", restate.TerminalError(runErr, 403)
				}
				return "", runErr
			}
			return id, nil
		})
		if createErr != nil {
			state.Status = types.StatusError
			state.Error = createErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return SNSTopicOutputs{}, createErr
		}
		topicArn = arn

		// Tag with managed key for ownership tracking.
		managedKey := restate.Key(ctx)
		if _, tagErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, topicArn, mergeTags(spec.Tags, map[string]string{
				"praxis:managed-key": managedKey,
			}))
		}); tagErr != nil {
			ctx.Log().Warn("failed to set managed-key tag", "topic", topicArn, "err", tagErr)
		}
	} else {
		// Topic exists — converge mutable attributes.
		if err := d.convergeAttributes(ctx, api, topicArn, spec, state.Desired); err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return SNSTopicOutputs{}, err
		}

		// Converge tags.
		if !drivers.TagsMatch(spec.Tags, state.Observed.Tags) {
			if _, tagErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.UpdateTags(rc, topicArn, mergeTags(
					spec.Tags, map[string]string{"praxis:managed-key": restate.Key(ctx)},
				))
			}); tagErr != nil {
				state.Status = types.StatusError
				state.Error = tagErr.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return SNSTopicOutputs{}, tagErr
			}
		}
	}

	// Refresh observed state.
	observed, obsErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		o, runErr := api.GetTopicAttributes(rc, topicArn)
		if runErr != nil {
			return ObservedState{}, runErr
		}
		return o, nil
	})
	if obsErr != nil {
		// Non-fatal — use minimal observed state from what we know.
		observed = ObservedState{TopicArn: topicArn, TopicName: spec.TopicName}
	}

	outputs := SNSTopicOutputs{
		TopicArn:  topicArn,
		TopicName: spec.TopicName,
		Owner:     observed.Owner,
	}

	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// convergeAttributes calls SetTopicAttribute for each mutable attribute that changed.
func (d *SNSTopicDriver) convergeAttributes(ctx restate.ObjectContext, api TopicAPI, topicArn string, desired, previous SNSTopicSpec) error {
	type attrUpdate struct {
		name string
		val  string
	}

	var updates []attrUpdate
	if desired.DisplayName != previous.DisplayName {
		updates = append(updates, attrUpdate{"DisplayName", desired.DisplayName})
	}
	if desired.Policy != "" && desired.Policy != previous.Policy {
		updates = append(updates, attrUpdate{"Policy", desired.Policy})
	}
	if desired.DeliveryPolicy != "" && desired.DeliveryPolicy != previous.DeliveryPolicy {
		updates = append(updates, attrUpdate{"DeliveryPolicy", desired.DeliveryPolicy})
	}
	if desired.KmsMasterKeyId != "" && desired.KmsMasterKeyId != previous.KmsMasterKeyId {
		updates = append(updates, attrUpdate{"KmsMasterKeyId", desired.KmsMasterKeyId})
	}
	if desired.ContentBasedDeduplication != previous.ContentBasedDeduplication {
		val := "false"
		if desired.ContentBasedDeduplication {
			val = "true"
		}
		updates = append(updates, attrUpdate{"ContentBasedDeduplication", val})
	}

	for _, u := range updates {
		name, val := u.name, u.val
		if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.SetTopicAttribute(rc, topicArn, name, val)
		}); err != nil {
			return fmt.Errorf("set attribute %s: %w", name, err)
		}
	}
	return nil
}

// Import adopts an existing SNS topic into Praxis state.
func (d *SNSTopicDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (SNSTopicOutputs, error) {
	ctx.Log().Info("importing SNS topic", "resourceId", ref.ResourceID, "mode", ref.Mode)

	api, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return SNSTopicOutputs{}, restate.TerminalError(err, 400)
	}

	// ResourceID can be a topic name or ARN.
	topicArn := ref.ResourceID
	if !strings.HasPrefix(topicArn, "arn:aws:sns:") {
		arn, findErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByName(rc, ref.ResourceID)
		})
		if findErr != nil {
			return SNSTopicOutputs{}, findErr
		}
		if arn == "" {
			return SNSTopicOutputs{}, restate.TerminalError(
				fmt.Errorf("topic %q not found", ref.ResourceID), 404)
		}
		topicArn = arn
	}

	observed, obsErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		o, runErr := api.GetTopicAttributes(rc, topicArn)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(
					fmt.Errorf("topic %q not found", topicArn), 404)
			}
			return ObservedState{}, runErr
		}
		return o, nil
	})
	if obsErr != nil {
		return SNSTopicOutputs{}, obsErr
	}

	spec := specFromObserved(observed, ref)
	outputs := SNSTopicOutputs{
		TopicArn:  topicArn,
		TopicName: observed.TopicName,
		Owner:     observed.Owner,
	}

	mode := types.ModeObserved
	if ref.Mode != "" {
		mode = ref.Mode
	}

	state, err := restate.Get[SNSTopicState](ctx, drivers.StateKey)
	if err != nil {
		return SNSTopicOutputs{}, err
	}
	state.Generation++
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Mode = mode
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Delete removes an SNS topic.
func (d *SNSTopicDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting SNS topic", "key", restate.Key(ctx))

	state, err := restate.Get[SNSTopicState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(
			fmt.Errorf("cannot delete topic %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.TopicArn), 409)
	}

	state.Status = types.StatusDeleting
	restate.Set(ctx, drivers.StateKey, state)

	api, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteTopic(rc, state.Outputs.TopicArn)
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
		return restate.Void{}, nil
	}); err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}

	state.Status = types.StatusDeleted
	restate.Set(ctx, drivers.StateKey, state)
	restate.Clear(ctx, drivers.StateKey)
	return nil
}

// Reconcile checks and corrects drift on the SNS topic.
func (d *SNSTopicDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[SNSTopicState](ctx, drivers.StateKey)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	if state.Status == types.StatusDeleted || state.Outputs.TopicArn == "" {
		return types.ReconcileResult{}, nil
	}

	state.ReconcileScheduled = false
	now := time.Now().UTC().Format(time.RFC3339)

	api, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}

	observed, obsErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		o, runErr := api.GetTopicAttributes(rc, state.Outputs.TopicArn)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, runErr
		}
		return o, nil
	})
	if obsErr != nil {
		if IsNotFound(obsErr) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("topic %s was deleted externally", state.Outputs.TopicArn)
			state.LastReconcile = now
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventExternalDelete, state.Error)
			return types.ReconcileResult{Error: state.Error}, nil
		}
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: obsErr.Error()}, nil
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
		ctx.Log().Info("drift detected, correcting", "topic", state.Outputs.TopicArn)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")

		corrErr := d.convergeAttributes(ctx, api, state.Outputs.TopicArn, state.Desired, specFromObserved(observed, types.ImportRef{}))
		if corrErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: corrErr.Error()}, nil
		}

		if !drivers.TagsMatch(state.Desired.Tags, observed.Tags) {
			if _, tagErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.UpdateTags(rc, state.Outputs.TopicArn, mergeTags(
					state.Desired.Tags, map[string]string{"praxis:managed-key": restate.Key(ctx)},
				))
			}); tagErr != nil {
				restate.Set(ctx, drivers.StateKey, state)
				d.scheduleReconcile(ctx, &state)
				return types.ReconcileResult{Drift: true, Correcting: true, Error: tagErr.Error()}, nil
			}
		}

		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}

	if drift && state.Mode == types.ModeObserved {
		ctx.Log().Info("drift detected (observed mode, not correcting)", "topic", state.Outputs.TopicArn)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		return types.ReconcileResult{Drift: true}, nil
	}

	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

// GetStatus returns the current resource status (concurrent-safe shared handler).
func (d *SNSTopicDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[SNSTopicState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{
		Status:     state.Status,
		Mode:       state.Mode,
		Generation: state.Generation,
		Error:      state.Error,
	}, nil
}

// GetOutputs returns the current topic outputs (concurrent-safe shared handler).
func (d *SNSTopicDriver) GetOutputs(ctx restate.ObjectSharedContext) (SNSTopicOutputs, error) {
	state, err := restate.Get[SNSTopicState](ctx, drivers.StateKey)
	if err != nil {
		return SNSTopicOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *SNSTopicDriver) GetInputs(ctx restate.ObjectSharedContext) (SNSTopicSpec, error) {
	state, err := restate.Get[SNSTopicState](ctx, drivers.StateKey)
	if err != nil {
		return SNSTopicSpec{}, err
	}
	return state.Desired, nil
}

func (d *SNSTopicDriver) scheduleReconcile(ctx restate.ObjectContext, state *SNSTopicState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *SNSTopicDriver) apiForAccount(ctx restate.Context, account string) (TopicAPI, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, fmt.Errorf("SNSTopicDriver is not configured with an auth client")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve SNS account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), nil
}

func specFromObserved(obs ObservedState, ref types.ImportRef) SNSTopicSpec {
	// Extract region from the topic ARN: arn:aws:sns:<region>:<account>:<topicName>
	region := ""
	if parts := strings.SplitN(obs.TopicArn, ":", 7); len(parts) >= 5 {
		region = parts[3]
	}
	spec := SNSTopicSpec{
		Account:                   ref.Account,
		Region:                    region,
		TopicName:                 obs.TopicName,
		DisplayName:               obs.DisplayName,
		FifoTopic:                 obs.FifoTopic,
		ContentBasedDeduplication: obs.ContentBasedDeduplication,
		Policy:                    obs.Policy,
		DeliveryPolicy:            obs.DeliveryPolicy,
		KmsMasterKeyId:            obs.KmsMasterKeyId,
		Tags:                      drivers.FilterPraxisTags(obs.Tags),
	}
	return spec
}
