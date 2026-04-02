package snssub

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

// SNSSubscriptionDriver is the Restate Virtual Object driver for SNS subscriptions.
type SNSSubscriptionDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) SubscriptionAPI
}

// NewSNSSubscriptionDriver returns a driver configured with real AWS credentials.
func NewSNSSubscriptionDriver(auth authservice.AuthClient) *SNSSubscriptionDriver {
	return NewSNSSubscriptionDriverWithFactory(auth, func(cfg aws.Config) SubscriptionAPI {
		return NewSubscriptionAPI(awsclient.NewSNSClient(cfg))
	})
}

// NewSNSSubscriptionDriverWithFactory returns a driver with an injectable API factory (for tests).
func NewSNSSubscriptionDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) SubscriptionAPI) *SNSSubscriptionDriver {
	if factory == nil {
		factory = func(cfg aws.Config) SubscriptionAPI {
			return NewSubscriptionAPI(awsclient.NewSNSClient(cfg))
		}
	}
	return &SNSSubscriptionDriver{auth: auth, apiFactory: factory}
}

func (SNSSubscriptionDriver) ServiceName() string { return ServiceName }

// Provision creates or converges an SNS subscription.
func (d *SNSSubscriptionDriver) Provision(ctx restate.ObjectContext, spec SNSSubscriptionSpec) (SNSSubscriptionOutputs, error) {
	ctx.Log().Info("provisioning SNS subscription", "key", restate.Key(ctx))

	if err := validateProtocolConstraints(spec); err != nil {
		return SNSSubscriptionOutputs{}, restate.TerminalError(err, 400)
	}

	api, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return SNSSubscriptionOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[SNSSubscriptionState](ctx, drivers.StateKey)
	if err != nil {
		return SNSSubscriptionOutputs{}, err
	}

	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++
	restate.Set(ctx, drivers.StateKey, state)

	// If we have an existing subscription ARN, check if it still exists.
	existingArn := state.Outputs.SubscriptionArn
	if existingArn != "" && !state.Observed.PendingConfirmation {
		obs, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			o, runErr := api.GetSubscriptionAttributes(rc, existingArn)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, nil // gone; resubscribe
				}
				return ObservedState{}, runErr
			}
			return o, nil
		})
		if descErr != nil {
			state.Status = types.StatusError
			state.Error = descErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return SNSSubscriptionOutputs{}, descErr
		}
		if obs.SubscriptionArn == "" {
			existingArn = "" // subscription gone; resubscribe
		} else {
			// Converge mutable attributes.
			if err := d.convergeAttributes(ctx, api, existingArn, spec, state.Desired); err != nil {
				state.Status = types.StatusError
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return SNSSubscriptionOutputs{}, err
			}

			// Refresh observed state.
			refreshed, obsErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
				return api.GetSubscriptionAttributes(rc, existingArn)
			})
			if obsErr != nil {
				refreshed = obs
			}

			outputs := SNSSubscriptionOutputs{
				SubscriptionArn: existingArn,
				TopicArn:        spec.TopicArn,
				Protocol:        spec.Protocol,
				Endpoint:        spec.Endpoint,
				Owner:           refreshed.Owner,
			}
			state.Observed = refreshed
			state.Outputs = outputs
			state.Status = types.StatusReady
			state.Error = ""
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return outputs, nil
		}
	}

	// Subscribe (idempotent — same topic+protocol+endpoint returns existing ARN).
	subscriptionArn, subErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		id, runErr := api.Subscribe(rc, spec)
		if runErr != nil {
			if IsInvalidParameter(runErr) {
				return "", restate.TerminalError(runErr, 400)
			}
			if isSubscriptionLimitExceeded(runErr) {
				return "", restate.TerminalError(runErr, 429)
			}
			return "", runErr
		}
		return id, nil
	})
	if subErr != nil {
		state.Status = types.StatusError
		state.Error = subErr.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return SNSSubscriptionOutputs{}, subErr
	}

	// Some protocols return "pending confirmation" until the endpoint confirms.
	isPending := subscriptionArn == "PendingConfirmation" || subscriptionArn == "pending confirmation"
	if isPending {
		// Try to find the real ARN for the pending subscription.
		realArn, findErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByTopicProtocolEndpoint(rc, spec.TopicArn, spec.Protocol, spec.Endpoint)
		})
		if findErr == nil && realArn != "" {
			subscriptionArn = realArn
		}
	}

	// Build observed state.
	var observed ObservedState
	if !isPending && subscriptionArn != "" {
		obs, obsErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.GetSubscriptionAttributes(rc, subscriptionArn)
		})
		if obsErr != nil {
			observed = ObservedState{
				SubscriptionArn: subscriptionArn,
				TopicArn:        spec.TopicArn,
				Protocol:        spec.Protocol,
				Endpoint:        spec.Endpoint,
			}
		} else {
			observed = obs
		}
	} else {
		observed = ObservedState{
			SubscriptionArn:     subscriptionArn,
			TopicArn:            spec.TopicArn,
			Protocol:            spec.Protocol,
			Endpoint:            spec.Endpoint,
			PendingConfirmation: true,
			ConfirmationStatus:  "pending",
		}
	}

	status := types.StatusReady
	if isPending {
		status = types.StatusPending
	}

	outputs := SNSSubscriptionOutputs{
		SubscriptionArn: subscriptionArn,
		TopicArn:        spec.TopicArn,
		Protocol:        spec.Protocol,
		Endpoint:        spec.Endpoint,
		Owner:           observed.Owner,
	}

	state.Observed = observed
	state.Outputs = outputs
	state.Status = status
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// convergeAttributes updates each subscription attribute that has changed.
func (d *SNSSubscriptionDriver) convergeAttributes(ctx restate.ObjectContext, api SubscriptionAPI, subscriptionArn string, desired, previous SNSSubscriptionSpec) error {
	type attrUpdate struct {
		name string
		val  string
	}

	var updates []attrUpdate
	if desired.FilterPolicy != previous.FilterPolicy {
		val := desired.FilterPolicy
		if val == "" {
			val = "{}"
		}
		updates = append(updates, attrUpdate{"FilterPolicy", val})
	}
	if desired.FilterPolicyScope != previous.FilterPolicyScope {
		updates = append(updates, attrUpdate{"FilterPolicyScope", desired.FilterPolicyScope})
	}
	if desired.RawMessageDelivery != previous.RawMessageDelivery {
		val := "false"
		if desired.RawMessageDelivery {
			val = "true"
		}
		updates = append(updates, attrUpdate{"RawMessageDelivery", val})
	}
	if desired.DeliveryPolicy != previous.DeliveryPolicy {
		val := desired.DeliveryPolicy
		if val == "" {
			val = "{}"
		}
		updates = append(updates, attrUpdate{"DeliveryPolicy", val})
	}
	if desired.RedrivePolicy != previous.RedrivePolicy {
		val := desired.RedrivePolicy
		if val == "" {
			val = "{}"
		}
		updates = append(updates, attrUpdate{"RedrivePolicy", val})
	}
	if desired.SubscriptionRoleArn != previous.SubscriptionRoleArn {
		updates = append(updates, attrUpdate{"SubscriptionRoleArn", desired.SubscriptionRoleArn})
	}

	for _, u := range updates {
		name, val := u.name, u.val
		if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.SetSubscriptionAttribute(rc, subscriptionArn, name, val)
		}); err != nil {
			return fmt.Errorf("set attribute %s: %w", name, err)
		}
	}
	return nil
}

// Import adopts an existing SNS subscription into Praxis state.
func (d *SNSSubscriptionDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (SNSSubscriptionOutputs, error) {
	ctx.Log().Info("importing SNS subscription", "resourceId", ref.ResourceID, "mode", ref.Mode)

	if !strings.HasPrefix(ref.ResourceID, "arn:aws:sns:") {
		return SNSSubscriptionOutputs{}, restate.TerminalError(
			fmt.Errorf("SNSSubscription import requires a subscription ARN as resourceID, got: %s", ref.ResourceID), 400)
	}

	api, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return SNSSubscriptionOutputs{}, restate.TerminalError(err, 400)
	}

	observed, obsErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		o, runErr := api.GetSubscriptionAttributes(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(
					fmt.Errorf("subscription %q not found", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return o, nil
	})
	if obsErr != nil {
		return SNSSubscriptionOutputs{}, obsErr
	}

	spec := specFromObserved(observed, ref)
	outputs := SNSSubscriptionOutputs{
		SubscriptionArn: ref.ResourceID,
		TopicArn:        observed.TopicArn,
		Protocol:        observed.Protocol,
		Endpoint:        observed.Endpoint,
		Owner:           observed.Owner,
	}

	mode := types.ModeObserved
	if ref.Mode != "" {
		mode = ref.Mode
	}

	state, err := restate.Get[SNSSubscriptionState](ctx, drivers.StateKey)
	if err != nil {
		return SNSSubscriptionOutputs{}, err
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

// Delete removes an SNS subscription.
func (d *SNSSubscriptionDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting SNS subscription", "key", restate.Key(ctx))

	state, err := restate.Get[SNSSubscriptionState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(
			fmt.Errorf("cannot delete subscription %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.SubscriptionArn), 409)
	}

	state.Status = types.StatusDeleting
	restate.Set(ctx, drivers.StateKey, state)

	api, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.Unsubscribe(rc, state.Outputs.SubscriptionArn)
		if runErr != nil && !IsNotFound(runErr) {
			if IsInvalidParameter(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
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

// Reconcile checks and corrects drift on the SNS subscription.
func (d *SNSSubscriptionDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[SNSSubscriptionState](ctx, drivers.StateKey)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	if state.Status == types.StatusDeleted || state.Outputs.SubscriptionArn == "" {
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

	// Handle pending confirmation — check if subscription was confirmed.
	if state.Observed.PendingConfirmation {
		realArn, findErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByTopicProtocolEndpoint(rc, state.Desired.TopicArn, state.Desired.Protocol, state.Desired.Endpoint)
		})
		if findErr != nil || realArn == "" {
			state.LastReconcile = now
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{}, nil
		}
		state.Outputs.SubscriptionArn = realArn
	}

	observed, obsErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		o, runErr := api.GetSubscriptionAttributes(rc, state.Outputs.SubscriptionArn)
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
			state.Error = fmt.Sprintf("subscription %s was deleted externally", state.Outputs.SubscriptionArn)
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

	// Update pending confirmation status if subscription was confirmed.
	if !observed.PendingConfirmation && state.Status == types.StatusPending {
		state.Status = types.StatusReady
	}

	drift := HasDrift(state.Desired, observed)

	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift}, nil
	}

	if drift && state.Mode == types.ModeManaged {
		if observed.PendingConfirmation {
			// Cannot update attributes on pending subscriptions.
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true}, nil
		}

		ctx.Log().Info("drift detected, correcting", "subscription", state.Outputs.SubscriptionArn)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")

		corrErr := d.convergeAttributes(ctx, api, state.Outputs.SubscriptionArn, state.Desired, specFromObserved(observed, types.ImportRef{}))
		if corrErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: corrErr.Error()}, nil
		}

		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}

	if drift && state.Mode == types.ModeObserved {
		ctx.Log().Info("drift detected (observed mode, not correcting)", "subscription", state.Outputs.SubscriptionArn)
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
func (d *SNSSubscriptionDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[SNSSubscriptionState](ctx, drivers.StateKey)
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

// GetOutputs returns the current subscription outputs (concurrent-safe shared handler).
func (d *SNSSubscriptionDriver) GetOutputs(ctx restate.ObjectSharedContext) (SNSSubscriptionOutputs, error) {
	state, err := restate.Get[SNSSubscriptionState](ctx, drivers.StateKey)
	if err != nil {
		return SNSSubscriptionOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *SNSSubscriptionDriver) scheduleReconcile(ctx restate.ObjectContext, state *SNSSubscriptionState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *SNSSubscriptionDriver) apiForAccount(ctx restate.Context, account string) (SubscriptionAPI, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, fmt.Errorf("SNSSubscriptionDriver is not configured with an auth client")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve SNS account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), nil
}

func validateProtocolConstraints(spec SNSSubscriptionSpec) error {
	switch spec.Protocol {
	case "lambda":
		if !strings.HasPrefix(spec.Endpoint, "arn:aws:lambda:") {
			return fmt.Errorf("lambda protocol requires a Lambda function ARN as endpoint")
		}
	case "sqs":
		if !strings.HasPrefix(spec.Endpoint, "arn:aws:sqs:") {
			return fmt.Errorf("sqs protocol requires an SQS queue ARN as endpoint")
		}
	case "firehose":
		if !strings.HasPrefix(spec.Endpoint, "arn:aws:firehose:") {
			return fmt.Errorf("firehose protocol requires a Firehose delivery stream ARN as endpoint")
		}
		if spec.SubscriptionRoleArn == "" {
			return fmt.Errorf("firehose protocol requires a subscriptionRoleArn")
		}
	case "email", "email-json":
		if !strings.Contains(spec.Endpoint, "@") {
			return fmt.Errorf("%s protocol requires an email address as endpoint", spec.Protocol)
		}
	case "http":
		if !strings.HasPrefix(spec.Endpoint, "http://") {
			return fmt.Errorf("http protocol requires an HTTP URL as endpoint")
		}
	case "https":
		if !strings.HasPrefix(spec.Endpoint, "https://") {
			return fmt.Errorf("https protocol requires an HTTPS URL as endpoint")
		}
	case "sms":
		if !strings.HasPrefix(spec.Endpoint, "+") {
			return fmt.Errorf("sms protocol requires a phone number in E.164 format")
		}
	}

	if spec.RawMessageDelivery {
		switch spec.Protocol {
		case "sqs", "http", "https", "firehose":
			// valid
		default:
			return fmt.Errorf("rawMessageDelivery is only supported for sqs, http, https, and firehose protocols")
		}
	}

	if spec.DeliveryPolicy != "" {
		switch spec.Protocol {
		case "http", "https":
			// valid
		default:
			return fmt.Errorf("deliveryPolicy is only supported for http and https protocols")
		}
	}

	return nil
}

func specFromObserved(obs ObservedState, ref types.ImportRef) SNSSubscriptionSpec {
	// Extract region from the subscription ARN: arn:aws:sns:<region>:<account>:<topicName>:<subId>
	region := ""
	if parts := strings.SplitN(obs.SubscriptionArn, ":", 8); len(parts) >= 5 {
		region = parts[3]
	}
	return SNSSubscriptionSpec{
		Account:             ref.Account,
		Region:              region,
		TopicArn:            obs.TopicArn,
		Protocol:            obs.Protocol,
		Endpoint:            obs.Endpoint,
		FilterPolicy:        obs.FilterPolicy,
		FilterPolicyScope:   obs.FilterPolicyScope,
		RawMessageDelivery:  obs.RawMessageDelivery,
		DeliveryPolicy:      obs.DeliveryPolicy,
		RedrivePolicy:       obs.RedrivePolicy,
		SubscriptionRoleArn: obs.SubscriptionRoleArn,
	}
}
