package iamuser

import (
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type IAMUserDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) IAMUserAPI
}

func NewIAMUserDriver(auth authservice.AuthClient) *IAMUserDriver {
	return NewIAMUserDriverWithFactory(auth, func(cfg aws.Config) IAMUserAPI {
		return NewIAMUserAPI(awsclient.NewIAMClient(cfg))
	})
}

func NewIAMUserDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) IAMUserAPI) *IAMUserDriver {
	if factory == nil {
		factory = func(cfg aws.Config) IAMUserAPI {
			return NewIAMUserAPI(awsclient.NewIAMClient(cfg))
		}
	}
	return &IAMUserDriver{auth: auth, apiFactory: factory}
}

func (d *IAMUserDriver) ServiceName() string {
	return ServiceName
}

func (d *IAMUserDriver) Provision(ctx restate.ObjectContext, spec IAMUserSpec) (IAMUserOutputs, error) {
	ctx.Log().Info("provisioning iam user", "key", restate.Key(ctx), "userName", spec.UserName)
	api, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return IAMUserOutputs{}, restate.TerminalError(err, 400)
	}

	spec = applyDefaults(spec)
	if spec.UserName == "" {
		return IAMUserOutputs{}, restate.TerminalError(fmt.Errorf("userName is required"), 400)
	}

	state, err := restate.Get[IAMUserState](ctx, drivers.StateKey)
	if err != nil {
		return IAMUserOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	outputs := state.Outputs
	userExists := outputs.UserName != "" || outputs.Arn != ""
	currentObserved := state.Observed
	if userExists {
		descResult, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribeUser(rc, spec.UserName)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 404)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr != nil || descResult.Arn == "" {
			userExists = false
		} else {
			currentObserved = descResult
			state.Observed = descResult
			outputs = outputsFromObserved(descResult)
		}
	}

	if !userExists {
		created, runErr := restate.Run(ctx, func(rc restate.RunContext) (IAMUserOutputs, error) {
			arn, userID, createErr := api.CreateUser(rc, spec)
			if createErr != nil {
				if IsAlreadyExists(createErr) {
					return IAMUserOutputs{}, restate.TerminalError(createErr, 409)
				}
				return IAMUserOutputs{}, createErr
			}
			return IAMUserOutputs{Arn: arn, UserId: userID, UserName: spec.UserName}, nil
		})
		if runErr != nil {
			state.Status = types.StatusError
			state.Error = runErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return IAMUserOutputs{}, runErr
		}
		outputs = created

		observedAfterCreate, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribeUser(rc, spec.UserName)
			if runErr != nil {
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr != nil {
			state.Status = types.StatusError
			state.Error = descErr.Error()
			state.Outputs = outputs
			restate.Set(ctx, drivers.StateKey, state)
			return IAMUserOutputs{}, descErr
		}
		currentObserved = observedAfterCreate
	}

	if correctionErr := d.correctDrift(ctx, api, spec.UserName, spec, currentObserved); correctionErr != nil {
		state.Status = types.StatusError
		state.Error = correctionErr.Error()
		state.Outputs = outputs
		restate.Set(ctx, drivers.StateKey, state)
		return IAMUserOutputs{}, correctionErr
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeUser(rc, spec.UserName)
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
		return IAMUserOutputs{}, err
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

func (d *IAMUserDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (IAMUserOutputs, error) {
	ctx.Log().Info("importing iam user", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return IAMUserOutputs{}, restate.TerminalError(err, 400)
	}

	mode := ref.Mode
	if mode == "" {
		mode = types.ModeObserved
	}

	state, err := restate.Get[IAMUserState](ctx, drivers.StateKey)
	if err != nil {
		return IAMUserOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeUser(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: user %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return IAMUserOutputs{}, err
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

func (d *IAMUserDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting iam user", "key", restate.Key(ctx))
	state, err := restate.Get[IAMUserState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete iam user in Observed mode; re-import with --mode managed to allow deletion"), 409)
	}

	name := state.Desired.UserName
	if name == "" {
		name = state.Outputs.UserName
	}
	if name == "" {
		restate.Set(ctx, drivers.StateKey, IAMUserState{Status: types.StatusDeleted})
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
		observed, descErr := api.DescribeUser(rc, name)
		if descErr != nil {
			if IsNotFound(descErr) {
				return restate.Void{}, nil
			}
			return restate.Void{}, descErr
		}

		for _, groupName := range observed.Groups {
			if runErr := api.RemoveUserFromGroup(rc, name, groupName); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
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
		if observed.PermissionsBoundary != "" {
			if runErr := api.DeleteUserPermissionsBoundary(rc, name); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
		}
		if runErr := api.DeleteLoginProfile(rc, name); runErr != nil && !IsNotFound(runErr) {
			return restate.Void{}, runErr
		}
		if runErr := api.DeleteAllAccessKeys(rc, name); runErr != nil && !IsNotFound(runErr) {
			return restate.Void{}, runErr
		}
		if runErr := api.DeleteUser(rc, name); runErr != nil {
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

	restate.Set(ctx, drivers.StateKey, IAMUserState{Status: types.StatusDeleted})
	return nil
}

func (d *IAMUserDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[IAMUserState](ctx, drivers.StateKey)
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

	name := state.Outputs.UserName
	if name == "" {
		name = state.Desired.UserName
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
		obs, runErr := api.DescribeUser(rc, name)
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
			state.Error = fmt.Sprintf("user %s was deleted externally", name)
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
		ctx.Log().Info("drift detected, correcting iam user", "userName", name)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		correctionErr := d.correctDrift(ctx, api, name, state.Desired, observed)
		if correctionErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		refreshed, refreshErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.DescribeUser(rc, name)
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
		ctx.Log().Info("drift detected (observed mode, not correcting)", "userName", name)
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

func (d *IAMUserDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[IAMUserState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *IAMUserDriver) GetOutputs(ctx restate.ObjectSharedContext) (IAMUserOutputs, error) {
	state, err := restate.Get[IAMUserState](ctx, drivers.StateKey)
	if err != nil {
		return IAMUserOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *IAMUserDriver) correctDrift(ctx restate.ObjectContext, api IAMUserAPI, userName string, desired IAMUserSpec, observed ObservedState) error {
	if desired.Path != observed.Path {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateUserPath(rc, userName, desired.Path)
		})
		if err != nil {
			return fmt.Errorf("update user path: %w", err)
		}
	}

	if desired.PermissionsBoundary != observed.PermissionsBoundary {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if desired.PermissionsBoundary == "" {
				if runErr := api.DeleteUserPermissionsBoundary(rc, userName); runErr != nil && !IsNotFound(runErr) {
					return restate.Void{}, runErr
				}
				return restate.Void{}, nil
			}
			return restate.Void{}, api.PutUserPermissionsBoundary(rc, userName, desired.PermissionsBoundary)
		})
		if err != nil {
			return fmt.Errorf("update permissions boundary: %w", err)
		}
	}

	for policyName, document := range normalizePolicyMap(desired.InlinePolicies) {
		currentDoc, ok := normalizePolicyMap(observed.InlinePolicies)[policyName]
		if ok && currentDoc == document {
			continue
		}
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.PutInlinePolicy(rc, userName, policyName, document); runErr != nil {
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
			if runErr := api.DeleteInlinePolicy(rc, userName, policyName); runErr != nil && !IsNotFound(runErr) {
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
			return restate.Void{}, api.AttachManagedPolicy(rc, userName, policyArn)
		})
		if err != nil {
			return fmt.Errorf("attach managed policy %s: %w", policyArn, err)
		}
	}
	for _, policyArn := range managedToRemove {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.DetachManagedPolicy(rc, userName, policyArn); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("detach managed policy %s: %w", policyArn, err)
		}
	}

	groupsToAdd, groupsToRemove := diffStringSets(desired.Groups, observed.Groups)
	for _, groupName := range groupsToAdd {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.AddUserToGroup(rc, userName, groupName)
		})
		if err != nil {
			return fmt.Errorf("add user to group %s: %w", groupName, err)
		}
	}
	for _, groupName := range groupsToRemove {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.RemoveUserFromGroup(rc, userName, groupName); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("remove user from group %s: %w", groupName, err)
		}
	}

	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, userName, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update user tags: %w", err)
		}
	}

	return nil
}

func (d *IAMUserDriver) scheduleReconcile(ctx restate.ObjectContext, state *IAMUserState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *IAMUserDriver) apiForAccount(ctx restate.ObjectContext, account string) (IAMUserAPI, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, fmt.Errorf("iam user driver is not configured")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAM account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), nil
}

func applyDefaults(spec IAMUserSpec) IAMUserSpec {
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.InlinePolicies == nil {
		spec.InlinePolicies = map[string]string{}
	}
	if spec.ManagedPolicyArns == nil {
		spec.ManagedPolicyArns = []string{}
	}
	if spec.Groups == nil {
		spec.Groups = []string{}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func specFromObserved(obs ObservedState) IAMUserSpec {
	inlinePolicies := make(map[string]string, len(obs.InlinePolicies))
	for key, value := range obs.InlinePolicies {
		inlinePolicies[key] = normalizePolicyDocument(value)
	}
	return IAMUserSpec{
		Path:                obs.Path,
		UserName:            obs.UserName,
		PermissionsBoundary: obs.PermissionsBoundary,
		InlinePolicies:      inlinePolicies,
		ManagedPolicyArns:   sortedStrings(obs.ManagedPolicyArns),
		Groups:              sortedStrings(obs.Groups),
		Tags:                filterPraxisTags(obs.Tags),
	}
}

func outputsFromObserved(obs ObservedState) IAMUserOutputs {
	return IAMUserOutputs{Arn: obs.Arn, UserId: obs.UserId, UserName: obs.UserName}
}

func diffStringSets(desired, observed []string) ([]string, []string) {
	desiredSet := make(map[string]struct{}, len(desired))
	observedSet := make(map[string]struct{}, len(observed))
	for _, value := range desired {
		desiredSet[value] = struct{}{}
	}
	for _, value := range observed {
		observedSet[value] = struct{}{}
	}
	var add []string
	for _, value := range desired {
		if _, ok := observedSet[value]; !ok {
			add = append(add, value)
		}
	}
	var remove []string
	for _, value := range observed {
		if _, ok := desiredSet[value]; !ok {
			remove = append(remove, value)
		}
	}
	return sortedStrings(add), sortedStrings(remove)
}

func stringSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	count := make(map[string]int, len(a))
	for _, value := range a {
		count[value]++
	}
	for _, value := range b {
		count[value]--
	}
	for _, value := range count {
		if value != 0 {
			return false
		}
	}
	return true
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
