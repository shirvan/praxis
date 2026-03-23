package listenerrule

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type ListenerRuleDriver struct {
	auth       *auth.Registry
	apiFactory func(aws.Config) ListenerRuleAPI
}

func NewListenerRuleDriver(accounts *auth.Registry) *ListenerRuleDriver {
	return NewListenerRuleDriverWithFactory(accounts, func(cfg aws.Config) ListenerRuleAPI {
		return NewListenerRuleAPI(awsclient.NewELBv2Client(cfg))
	})
}

func NewListenerRuleDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) ListenerRuleAPI) *ListenerRuleDriver {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	if factory == nil {
		factory = func(cfg aws.Config) ListenerRuleAPI {
			return NewListenerRuleAPI(awsclient.NewELBv2Client(cfg))
		}
	}
	return &ListenerRuleDriver{auth: accounts, apiFactory: factory}
}

func (d *ListenerRuleDriver) ServiceName() string { return ServiceName }

func (d *ListenerRuleDriver) Provision(ctx restate.ObjectContext, spec ListenerRuleSpec) (ListenerRuleOutputs, error) {
	ctx.Log().Info("provisioning listener rule", "key", restate.Key(ctx))
	api, _, err := d.apiForAccount(spec.Account)
	if err != nil {
		return ListenerRuleOutputs{}, restate.TerminalError(err, 400)
	}
	if err := validateSpec(spec); err != nil {
		return ListenerRuleOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[ListenerRuleState](ctx, drivers.StateKey)
	if err != nil {
		return ListenerRuleOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	if state.Outputs.RuleArn != "" {
		current, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, e := api.DescribeRule(rc, state.Outputs.RuleArn)
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
			return ListenerRuleOutputs{}, descErr
		}
		if current.RuleArn != "" {
			if hasImmutableChange(spec, current) {
				err := fmt.Errorf("listener rule %q requires replacement because immutable field listenerArn changed; delete and re-apply", state.Outputs.RuleArn)
				state.Observed = current
				state.Status = types.StatusError
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return ListenerRuleOutputs{}, restate.TerminalError(err, 409)
			}
			if corrErr := d.correctDrift(ctx, api, current.RuleArn, spec, current); corrErr != nil {
				state.Status = types.StatusError
				state.Error = corrErr.Error()
				state.Observed = current
				state.Outputs = outputsFromObserved(current)
				restate.Set(ctx, drivers.StateKey, state)
				return ListenerRuleOutputs{}, corrErr
			}
			refreshed, refreshErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
				return api.DescribeRule(rc, current.RuleArn)
			})
			if refreshErr != nil {
				state.Status = types.StatusError
				state.Error = refreshErr.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return ListenerRuleOutputs{}, refreshErr
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
		a, createErr := api.CreateRule(rc, spec.ListenerArn, spec)
		if createErr != nil {
			if IsPriorityInUse(createErr) {
				return "", restate.TerminalError(createErr, 409)
			}
			if IsTooMany(createErr) || IsTooManyConditions(createErr) {
				return "", restate.TerminalError(createErr, 503)
			}
			if IsTargetGroupNotFound(createErr) || IsInvalidConfig(createErr) {
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
		return ListenerRuleOutputs{}, runErr
	}

	observed, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.DescribeRule(rc, arn)
	})
	if descErr != nil {
		state.Status = types.StatusError
		state.Error = descErr.Error()
		state.Outputs = ListenerRuleOutputs{RuleArn: arn, Priority: spec.Priority}
		restate.Set(ctx, drivers.StateKey, state)
		return ListenerRuleOutputs{}, descErr
	}

	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

func (d *ListenerRuleDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (ListenerRuleOutputs, error) {
	ctx.Log().Info("importing listener rule", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, _, err := d.apiForAccount(ref.Account)
	if err != nil {
		return ListenerRuleOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[ListenerRuleState](ctx, drivers.StateKey)
	if err != nil {
		return ListenerRuleOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, descErr := api.DescribeRule(rc, ref.ResourceID)
		if descErr != nil {
			if IsNotFound(descErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: listener rule %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, descErr
		}
		return obs, nil
	})
	if err != nil {
		return ListenerRuleOutputs{}, err
	}
	if observed.IsDefault {
		return ListenerRuleOutputs{}, restate.TerminalError(fmt.Errorf("cannot import default rule %s: default rules are managed by the Listener driver", ref.ResourceID), 409)
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
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

func (d *ListenerRuleDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting listener rule", "key", restate.Key(ctx))
	state, err := restate.Get[ListenerRuleState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete listener rule %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.RuleArn), 409)
	}
	if state.Observed.IsDefault {
		return restate.TerminalError(fmt.Errorf("cannot delete default rule %s: default rules are managed by the Listener driver", state.Outputs.RuleArn), 409)
	}
	if state.Outputs.RuleArn == "" {
		restate.Set(ctx, drivers.StateKey, ListenerRuleState{Status: types.StatusDeleted})
		return nil
	}
	api, _, err := d.apiForAccount(state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}
	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteRule(rc, state.Outputs.RuleArn)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
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
	restate.Set(ctx, drivers.StateKey, ListenerRuleState{Status: types.StatusDeleted})
	return nil
}

func (d *ListenerRuleDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[ListenerRuleState](ctx, drivers.StateKey)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	api, _, err := d.apiForAccount(state.Desired.Account)
	if err != nil {
		return types.ReconcileResult{}, restate.TerminalError(err, 400)
	}
	state.ReconcileScheduled = false
	if state.Status != types.StatusReady && state.Status != types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	if state.Outputs.RuleArn == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) { return time.Now().UTC().Format(time.RFC3339), nil })
	if err != nil {
		return types.ReconcileResult{}, err
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, descErr := api.DescribeRule(rc, state.Outputs.RuleArn)
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
			state.Error = fmt.Sprintf("listener rule %s was deleted externally", state.Outputs.RuleArn)
			state.LastReconcile = now
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
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
		correctionErr := d.correctDrift(ctx, api, observed.RuleArn, state.Desired, observed)
		if correctionErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}
	if drift && state.Mode == types.ModeObserved {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true}, nil
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *ListenerRuleDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[ListenerRuleState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *ListenerRuleDriver) GetOutputs(ctx restate.ObjectSharedContext) (ListenerRuleOutputs, error) {
	state, err := restate.Get[ListenerRuleState](ctx, drivers.StateKey)
	if err != nil {
		return ListenerRuleOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *ListenerRuleDriver) correctDrift(ctx restate.ObjectContext, api ListenerRuleAPI, arn string, desired ListenerRuleSpec, observed ObservedState) error {
	// Priority change uses a separate API call
	if desired.Priority != observed.Priority {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.SetRulePriorities(rc, arn, desired.Priority)
		})
		if err != nil {
			return fmt.Errorf("set rule priorities: %w", err)
		}
	}
	// Conditions + actions change uses ModifyRule
	if !conditionsEqual(desired.Conditions, observed.Conditions) || !actionsEqual(desired.Actions, observed.Actions) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyRule(rc, arn, desired.Conditions, desired.Actions)
		})
		if err != nil {
			return fmt.Errorf("modify rule: %w", err)
		}
	}
	// Tags update
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

func (d *ListenerRuleDriver) scheduleReconcile(ctx restate.ObjectContext, state *ListenerRuleState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *ListenerRuleDriver) apiForAccount(account string) (ListenerRuleAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("ListenerRuleDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.Resolve(account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve listener rule account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func validateSpec(spec ListenerRuleSpec) error {
	if spec.ListenerArn == "" {
		return fmt.Errorf("listenerArn is required")
	}
	if spec.Priority < 1 || spec.Priority > 50000 {
		return fmt.Errorf("priority must be between 1 and 50000")
	}
	if len(spec.Conditions) == 0 {
		return fmt.Errorf("at least one condition is required")
	}
	if len(spec.Actions) == 0 {
		return fmt.Errorf("at least one action is required")
	}
	return nil
}

func hasImmutableChange(desired ListenerRuleSpec, observed ObservedState) bool {
	return desired.ListenerArn != observed.ListenerArn
}

func specFromObserved(observed ObservedState) ListenerRuleSpec {
	return ListenerRuleSpec{
		ListenerArn: observed.ListenerArn,
		Priority:    observed.Priority,
		Conditions:  observed.Conditions,
		Actions:     observed.Actions,
		Tags:        filterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) ListenerRuleOutputs {
	return ListenerRuleOutputs{
		RuleArn:  observed.RuleArn,
		Priority: observed.Priority,
	}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}
