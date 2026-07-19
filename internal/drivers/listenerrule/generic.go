package listenerrule

import (
	"fmt"
	"maps"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/drivers/kernel"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type genericOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) ListenerRuleAPI
}

// NewGenericListenerRuleDriver constructs the single alpha implementation of
// the ListenerRule lifecycle on the shared generic driver kernel.
func NewGenericListenerRuleDriver(auth authservice.AuthClient) *kernel.Driver[ListenerRuleSpec, ListenerRuleOutputs, ObservedState] {
	return newGenericListenerRuleDriverWithFactory(auth, nil)
}

func newGenericListenerRuleDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) ListenerRuleAPI) *kernel.Driver[ListenerRuleSpec, ListenerRuleOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) ListenerRuleAPI {
			return NewListenerRuleAPI(awsclient.NewELBv2Client(cfg))
		}
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[ListenerRuleSpec, ListenerRuleOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared:     true,
			Import:       true,
			ObservedMode: true,
			Delete:       true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec ListenerRuleSpec) (ListenerRuleSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return ListenerRuleSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec.Region = region
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) ListenerRuleSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ ListenerRuleOutputs) ListenerRuleOutputs {
			return outputsFromObserved(observed)
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired ListenerRuleSpec, outputs ListenerRuleOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	if outputs.RuleArn != "" {
		return observeListenerRule(ctx, api, outputs.RuleArn)
	}
	if desired.ListenerArn == "" || desired.Priority == 0 {
		return kernel.Observation[ObservedState]{}, nil
	}
	observed, err := findListenerRule(ctx, api, desired.ListenerArn, desired.Priority)
	if err != nil || !observed.Exists {
		return observed, err
	}
	if observed.Value.Tags["praxis:managed-key"] != desired.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(
			fmt.Errorf("refusing to adopt listener rule at priority %d without exact Praxis ownership tag %q", desired.Priority, desired.ManagedKey),
			409,
		)
	}
	return observed, nil
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired ListenerRuleSpec) (kernel.CreateResult[ListenerRuleOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[ListenerRuleOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	arn, err := drivers.RunAWS(ctx, func(runCtx restate.RunContext) (string, error) {
		observed, findErr := api.FindRuleByPriority(runCtx, desired.ListenerArn, desired.Priority)
		if findErr == nil {
			if observed.Tags["praxis:managed-key"] != desired.ManagedKey {
				return "", restate.TerminalError(
					fmt.Errorf("refusing to adopt listener rule without exact Praxis ownership tag %q", desired.ManagedKey),
					409,
				)
			}
			return observed.RuleArn, nil
		}
		if !IsNotFound(findErr) {
			return "", findErr
		}
		return api.CreateRule(runCtx, desired.ListenerArn, desired)
	}, classifyListenerRuleMutation)
	return kernel.CreateResult[ListenerRuleOutputs]{
		SeedOutputs: ListenerRuleOutputs{RuleArn: arn, Priority: desired.Priority},
	}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired ListenerRuleSpec, observed ObservedState, currentOutputs ListenerRuleOutputs) (ListenerRuleOutputs, error) {
	if desired.ListenerArn != observed.ListenerArn {
		return currentOutputs, restate.TerminalError(fmt.Errorf("listenerArn is immutable; delete and reprovision the listener rule"), 409)
	}
	if owner := observed.Tags["praxis:managed-key"]; owner != "" && owner != desired.ManagedKey {
		return currentOutputs, restate.TerminalError(fmt.Errorf("listener rule is owned by Praxis object %q", owner), 409)
	}
	if observed.IsDefault {
		return currentOutputs, restate.TerminalError(fmt.Errorf("default rules are managed by the Listener driver"), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return currentOutputs, drivers.ClassifyCredentialError(err)
	}
	if desired.Priority != observed.Priority {
		if _, err = drivers.RunAWS(ctx, func(runCtx restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.SetRulePriorities(runCtx, observed.RuleArn, desired.Priority)
		}, classifyListenerRuleMutation); err != nil {
			return currentOutputs, err
		}
	}
	if !conditionsEqual(desired.Conditions, observed.Conditions) || !actionsEqual(desired.Actions, observed.Actions) {
		if _, err = drivers.RunAWS(ctx, func(runCtx restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyRule(runCtx, observed.RuleArn, desired.Conditions, desired.Actions)
		}, classifyListenerRuleMutation); err != nil {
			return currentOutputs, err
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) || observed.Tags["praxis:managed-key"] != desired.ManagedKey {
		_, err = drivers.RunAWS(ctx, func(runCtx restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(runCtx, observed.RuleArn, listenerRuleManagedTags(desired.Tags, desired.ManagedKey))
		}, classifyListenerRuleMutation)
	}
	return currentOutputs, err
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired ListenerRuleSpec, outputs ListenerRuleOutputs) error {
	if outputs.RuleArn == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	observed, err := observeListenerRule(ctx, api, outputs.RuleArn)
	if err != nil || !observed.Exists {
		return err
	}
	if observed.Value.IsDefault {
		return restate.TerminalError(fmt.Errorf("default rules are managed by the Listener driver"), 409)
	}
	if owner := observed.Value.Tags["praxis:managed-key"]; owner != "" && owner != desired.ManagedKey {
		return restate.TerminalError(fmt.Errorf("refusing to delete listener rule owned by %q", owner), 409)
	}
	_, err = drivers.RunAWS(ctx, func(runCtx restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteRule(runCtx, outputs.RuleArn)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyListenerRuleMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	observed, err := observeListenerRule(ctx, api, ref.ResourceID)
	if err == nil && observed.Exists && observed.Value.IsDefault {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(
			fmt.Errorf("cannot import default rule %s: default rules are managed by the Listener driver", ref.ResourceID),
			409,
		)
	}
	return observed, err
}

func observeListenerRule(ctx restate.ObjectContext, api ListenerRuleAPI, arn string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(runCtx restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeRule(runCtx, arn)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyListenerRuleObserve)
}

func findListenerRule(ctx restate.ObjectContext, api ListenerRuleAPI, listenerArn string, priority int) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(runCtx restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.FindRuleByPriority(runCtx, listenerArn, priority)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyListenerRuleObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (ListenerRuleAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("ListenerRule driver is not configured with an auth client")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve listener rule account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyListenerRuleObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidConfig(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyListenerRuleMutation(err error) error {
	if err == nil || restate.IsTerminalError(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsPriorityInUse(err) {
		return restate.TerminalError(err, 409)
	}
	if IsTargetGroupNotFound(err) || IsInvalidConfig(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if IsTooMany(err) || IsTooManyConditions(err) {
		return restate.TerminalError(err, 503)
	}
	return err
}

func listenerRuleManagedTags(tags map[string]string, managedKey string) map[string]string {
	result := map[string]string{}
	maps.Copy(result, drivers.FilterPraxisTags(tags))
	if managedKey != "" {
		result["praxis:managed-key"] = managedKey
	}
	return result
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

func specFromObserved(observed ObservedState) ListenerRuleSpec {
	return ListenerRuleSpec{
		ListenerArn: observed.ListenerArn,
		Priority:    observed.Priority,
		Conditions:  observed.Conditions,
		Actions:     observed.Actions,
		Tags:        drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) ListenerRuleOutputs {
	return ListenerRuleOutputs{RuleArn: observed.RuleArn, Priority: observed.Priority}
}
