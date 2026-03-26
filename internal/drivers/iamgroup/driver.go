package iamgroup

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type IAMGroupDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) IAMGroupAPI
}

func NewIAMGroupDriver(auth authservice.AuthClient) *IAMGroupDriver {
	return NewIAMGroupDriverWithFactory(auth, func(cfg aws.Config) IAMGroupAPI {
		return NewIAMGroupAPI(awsclient.NewIAMClient(cfg))
	})
}

func NewIAMGroupDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) IAMGroupAPI) *IAMGroupDriver {
	if factory == nil {
		factory = func(cfg aws.Config) IAMGroupAPI {
			return NewIAMGroupAPI(awsclient.NewIAMClient(cfg))
		}
	}
	return &IAMGroupDriver{auth: auth, apiFactory: factory}
}

func (d *IAMGroupDriver) ServiceName() string {
	return ServiceName
}

func (d *IAMGroupDriver) Provision(ctx restate.ObjectContext, spec IAMGroupSpec) (IAMGroupOutputs, error) {
	ctx.Log().Info("provisioning iam group", "key", restate.Key(ctx), "groupName", spec.GroupName)
	api, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return IAMGroupOutputs{}, restate.TerminalError(err, 400)
	}

	spec = applyDefaults(spec)
	if spec.GroupName == "" {
		return IAMGroupOutputs{}, restate.TerminalError(fmt.Errorf("groupName is required"), 400)
	}

	state, err := restate.Get[IAMGroupState](ctx, drivers.StateKey)
	if err != nil {
		return IAMGroupOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	outputs := state.Outputs
	groupExists := outputs.GroupName != "" || outputs.Arn != ""
	currentObserved := state.Observed
	if groupExists {
		descResult, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribeGroup(rc, spec.GroupName)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 404)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr != nil || descResult.Arn == "" {
			groupExists = false
		} else {
			currentObserved = descResult
			state.Observed = descResult
			outputs = outputsFromObserved(descResult)
		}
	}

	if !groupExists {
		created, runErr := restate.Run(ctx, func(rc restate.RunContext) (IAMGroupOutputs, error) {
			arn, groupID, createErr := api.CreateGroup(rc, spec)
			if createErr != nil {
				if IsAlreadyExists(createErr) {
					return IAMGroupOutputs{}, restate.TerminalError(createErr, 409)
				}
				return IAMGroupOutputs{}, createErr
			}
			return IAMGroupOutputs{Arn: arn, GroupId: groupID, GroupName: spec.GroupName}, nil
		})
		if runErr != nil {
			state.Status = types.StatusError
			state.Error = runErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return IAMGroupOutputs{}, runErr
		}
		outputs = created

		observedAfterCreate, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.DescribeGroup(rc, spec.GroupName)
		})
		if descErr != nil {
			state.Status = types.StatusError
			state.Error = descErr.Error()
			state.Outputs = outputs
			restate.Set(ctx, drivers.StateKey, state)
			return IAMGroupOutputs{}, descErr
		}
		currentObserved = observedAfterCreate
	}

	if correctionErr := d.correctDrift(ctx, api, spec.GroupName, spec, currentObserved); correctionErr != nil {
		state.Status = types.StatusError
		state.Error = correctionErr.Error()
		state.Outputs = outputs
		restate.Set(ctx, drivers.StateKey, state)
		return IAMGroupOutputs{}, correctionErr
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeGroup(rc, spec.GroupName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = outputs
		restate.Set(ctx, drivers.StateKey, state)
		return IAMGroupOutputs{}, err
	}

	outputs = outputsFromObserved(observed)
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

func (d *IAMGroupDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (IAMGroupOutputs, error) {
	ctx.Log().Info("importing iam group", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return IAMGroupOutputs{}, restate.TerminalError(err, 400)
	}

	mode := ref.Mode
	if mode == "" {
		mode = types.ModeObserved
	}

	state, err := restate.Get[IAMGroupState](ctx, drivers.StateKey)
	if err != nil {
		return IAMGroupOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeGroup(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: group %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return IAMGroupOutputs{}, err
	}

	spec := specFromObserved(observed)
	spec.Account = ref.Account
	outputs := outputsFromObserved(observed)

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

func (d *IAMGroupDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting iam group", "key", restate.Key(ctx))
	state, err := restate.Get[IAMGroupState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete iam group in Observed mode; re-import with --mode managed to allow deletion"), 409)
	}

	name := state.Desired.GroupName
	if name == "" {
		name = state.Outputs.GroupName
	}
	if name == "" {
		restate.Set(ctx, drivers.StateKey, IAMGroupState{Status: types.StatusDeleted})
		return nil
	}

	api, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		observed, descErr := api.DescribeGroup(rc, name)
		if descErr != nil {
			if IsNotFound(descErr) {
				return restate.Void{}, nil
			}
			return restate.Void{}, descErr
		}

		if runErr := api.RemoveAllMembers(rc, name); runErr != nil && !IsNotFound(runErr) {
			return restate.Void{}, runErr
		}
		for _, policyArn := range observed.ManagedPolicyArns {
			if runErr := api.DetachManagedPolicy(rc, name, policyArn); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
		}
		for policyName := range observed.InlinePolicies {
			if runErr := api.DeleteInlinePolicy(rc, name, policyName); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
		}
		if runErr := api.DeleteGroup(rc, name); runErr != nil {
			if IsDeleteConflict(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 409)
			}
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

	restate.Set(ctx, drivers.StateKey, IAMGroupState{Status: types.StatusDeleted})
	return nil
}

func (d *IAMGroupDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[IAMGroupState](ctx, drivers.StateKey)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	api, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return types.ReconcileResult{}, restate.TerminalError(err, 400)
	}

	state.ReconcileScheduled = false
	if state.Status != types.StatusReady && state.Status != types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}

	name := state.Outputs.GroupName
	if name == "" {
		name = state.Desired.GroupName
	}
	if name == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}

	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return types.ReconcileResult{}, err
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeGroup(rc, name)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("group %s was deleted externally", name)
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
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}

	if drift && state.Mode == types.ModeManaged {
		ctx.Log().Info("drift detected, correcting iam group", "groupName", name)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		correctionErr := d.correctDrift(ctx, api, name, state.Desired, observed)
		if correctionErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		refreshed, refreshErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.DescribeGroup(rc, name)
		})
		if refreshErr == nil {
			state.Observed = refreshed
			state.Outputs = outputsFromObserved(refreshed)
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}

	if drift && state.Mode == types.ModeObserved {
		ctx.Log().Info("drift detected (observed mode, not correcting)", "groupName", name)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	state.Outputs = outputsFromObserved(observed)
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *IAMGroupDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[IAMGroupState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *IAMGroupDriver) GetOutputs(ctx restate.ObjectSharedContext) (IAMGroupOutputs, error) {
	state, err := restate.Get[IAMGroupState](ctx, drivers.StateKey)
	if err != nil {
		return IAMGroupOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *IAMGroupDriver) correctDrift(ctx restate.ObjectContext, api IAMGroupAPI, groupName string, desired IAMGroupSpec, observed ObservedState) error {
	if desired.Path != observed.Path {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateGroupPath(rc, groupName, desired.Path)
		})
		if err != nil {
			return fmt.Errorf("update group path: %w", err)
		}
	}

	for policyName, document := range normalizePolicyMap(desired.InlinePolicies) {
		currentDoc, ok := normalizePolicyMap(observed.InlinePolicies)[policyName]
		if ok && currentDoc == document {
			continue
		}
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.PutInlinePolicy(rc, groupName, policyName, document); runErr != nil {
				if IsMalformedPolicy(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("put inline policy %s: %w", policyName, err)
		}
	}
	for policyName := range observed.InlinePolicies {
		if _, ok := desired.InlinePolicies[policyName]; ok {
			continue
		}
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.DeleteInlinePolicy(rc, groupName, policyName); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("delete inline policy %s: %w", policyName, err)
		}
	}

	managedToAdd, managedToRemove := diffStringSets(desired.ManagedPolicyArns, observed.ManagedPolicyArns)
	for _, policyArn := range managedToAdd {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.AttachManagedPolicy(rc, groupName, policyArn)
		})
		if err != nil {
			return fmt.Errorf("attach managed policy %s: %w", policyArn, err)
		}
	}
	for _, policyArn := range managedToRemove {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.DetachManagedPolicy(rc, groupName, policyArn); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("detach managed policy %s: %w", policyArn, err)
		}
	}

	return nil
}

func (d *IAMGroupDriver) apiForAccount(ctx restate.ObjectContext, account string) (IAMGroupAPI, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, fmt.Errorf("iam group driver is not configured")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAM account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), nil
}

func (d *IAMGroupDriver) scheduleReconcile(ctx restate.ObjectContext, state *IAMGroupState) {
	if state == nil || state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func applyDefaults(spec IAMGroupSpec) IAMGroupSpec {
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.InlinePolicies == nil {
		spec.InlinePolicies = map[string]string{}
	}
	if spec.ManagedPolicyArns == nil {
		spec.ManagedPolicyArns = []string{}
	}
	return spec
}

func outputsFromObserved(observed ObservedState) IAMGroupOutputs {
	return IAMGroupOutputs{Arn: observed.Arn, GroupId: observed.GroupId, GroupName: observed.GroupName}
}

func specFromObserved(observed ObservedState) IAMGroupSpec {
	return IAMGroupSpec{
		Path:              observed.Path,
		GroupName:         observed.GroupName,
		InlinePolicies:    normalizePolicyMap(observed.InlinePolicies),
		ManagedPolicyArns: sortedStrings(observed.ManagedPolicyArns),
	}
}

func diffStringSets(desired, observed []string) ([]string, []string) {
	observedSet := make(map[string]struct{}, len(observed))
	for _, value := range observed {
		observedSet[value] = struct{}{}
	}
	var toAdd []string
	for _, value := range desired {
		if _, ok := observedSet[value]; !ok {
			toAdd = append(toAdd, value)
		}
	}
	desiredSet := make(map[string]struct{}, len(desired))
	for _, value := range desired {
		desiredSet[value] = struct{}{}
	}
	var toRemove []string
	for _, value := range observed {
		if _, ok := desiredSet[value]; !ok {
			toRemove = append(toRemove, value)
		}
	}
	return sortedStrings(toAdd), sortedStrings(toRemove)
}
