package natgw

import (
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type NATGatewayDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) NATGatewayAPI
}

func NewNATGatewayDriver(auth authservice.AuthClient) *NATGatewayDriver {
	return NewNATGatewayDriverWithFactory(auth, func(cfg aws.Config) NATGatewayAPI {
		return NewNATGatewayAPI(awsclient.NewEC2Client(cfg))
	})
}

func NewNATGatewayDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) NATGatewayAPI) *NATGatewayDriver {
	if factory == nil {
		factory = func(cfg aws.Config) NATGatewayAPI {
			return NewNATGatewayAPI(awsclient.NewEC2Client(cfg))
		}
	}
	return &NATGatewayDriver{auth: auth, apiFactory: factory}
}

func (d *NATGatewayDriver) ServiceName() string {
	return ServiceName
}

func (d *NATGatewayDriver) Provision(ctx restate.ObjectContext, spec NATGatewaySpec) (NATGatewayOutputs, error) {
	ctx.Log().Info("provisioning NAT gateway", "key", restate.Key(ctx))
	api, _, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return NATGatewayOutputs{}, restate.TerminalError(err, 400)
	}

	spec = applyDefaults(spec)
	if err := validateSpec(spec); err != nil {
		return NATGatewayOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[NATGatewayState](ctx, drivers.StateKey)
	if err != nil {
		return NATGatewayOutputs{}, err
	}

	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	natGatewayID := state.Outputs.NatGatewayId
	currentObserved := state.Observed
	if natGatewayID != "" {
		described, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribeNATGateway(rc, natGatewayID)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 404)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr != nil {
			if IsNotFound(descErr) {
				natGatewayID = ""
				currentObserved = ObservedState{}
			} else {
				state.Status = types.StatusError
				state.Error = descErr.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return NATGatewayOutputs{}, descErr
			}
		} else {
			currentObserved = described
			if IsFailed(described.State) {
				if err := d.deleteAndWait(ctx, api, described.NatGatewayId); err != nil {
					state.Status = types.StatusError
					state.Error = err.Error()
					state.Outputs = outputsFromObserved(described)
					restate.Set(ctx, drivers.StateKey, state)
					return NATGatewayOutputs{}, err
				}
				natGatewayID = ""
				currentObserved = ObservedState{}
			}
		}
	}

	if natGatewayID == "" && spec.ManagedKey != "" {
		conflictID, conflictErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, runErr := api.FindByManagedKey(rc, spec.ManagedKey)
			if runErr != nil {
				if strings.Contains(runErr.Error(), "ownership corruption") {
					return "", restate.TerminalError(runErr, 500)
				}
				return "", runErr
			}
			return id, nil
		})
		if conflictErr != nil {
			state.Status = types.StatusError
			state.Error = conflictErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return NATGatewayOutputs{}, conflictErr
		}
		if conflictID != "" {
			err := formatManagedKeyConflict(spec.ManagedKey, conflictID)
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return NATGatewayOutputs{}, restate.TerminalError(err, 409)
		}
	}

	created := false
	if natGatewayID == "" {
		createdID, createErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, runErr := api.CreateNATGateway(rc, spec)
			if runErr != nil {
				if IsInvalidParam(runErr) || IsSubnetNotFound(runErr) {
					return "", restate.TerminalError(runErr, 400)
				}
				if IsAllocationInUse(runErr) {
					return "", restate.TerminalError(runErr, 409)
				}
				return "", runErr
			}
			return id, nil
		})
		if createErr != nil {
			state.Status = types.StatusError
			state.Error = createErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return NATGatewayOutputs{}, createErr
		}
		natGatewayID = createdID
		created = true

		if err := d.waitUntilAvailable(ctx, api, natGatewayID, spec, &state); err != nil {
			return NATGatewayOutputs{}, err
		}
	} else if !tagsMatch(spec.Tags, currentObserved.Tags) {
		_, tagErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, natGatewayID, spec.Tags)
		})
		if tagErr != nil {
			state.Status = types.StatusError
			state.Error = tagErr.Error()
			state.Outputs = outputsFromObserved(currentObserved)
			restate.Set(ctx, drivers.StateKey, state)
			return NATGatewayOutputs{}, restate.TerminalError(tagErr, 500)
		}
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeNATGateway(rc, natGatewayID)
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
		state.Outputs = NATGatewayOutputs{NatGatewayId: natGatewayID, SubnetId: spec.SubnetId, ConnectivityType: spec.ConnectivityType, AllocationId: spec.AllocationId}
		restate.Set(ctx, drivers.StateKey, state)
		return NATGatewayOutputs{}, err
	}

	if IsFailed(observed.State) {
		err := failedStateError(observed)
		state.Observed = observed
		state.Outputs = outputsFromObserved(observed)
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return NATGatewayOutputs{}, err
	}

	if created {
		currentObserved = observed
	}

	outputs := outputsFromObserved(observed)
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

func (d *NATGatewayDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (NATGatewayOutputs, error) {
	ctx.Log().Info("importing NAT gateway", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return NATGatewayOutputs{}, restate.TerminalError(err, 400)
	}

	mode := defaultNATGatewayImportMode(ref.Mode)
	state, err := restate.Get[NATGatewayState](ctx, drivers.StateKey)
	if err != nil {
		return NATGatewayOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeNATGateway(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: NAT gateway %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return NATGatewayOutputs{}, err
	}

	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region
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

func (d *NATGatewayDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting NAT gateway", "key", restate.Key(ctx))
	state, err := restate.Get[NATGatewayState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(
			fmt.Errorf("cannot delete NAT gateway %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.NatGatewayId),
			409,
		)
	}

	natGatewayID := state.Outputs.NatGatewayId
	if natGatewayID == "" {
		restate.Set(ctx, drivers.StateKey, NATGatewayState{Status: types.StatusDeleted})
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
		runErr := api.DeleteNATGateway(rc, natGatewayID)
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

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.WaitUntilDeleted(rc, natGatewayID)
		if runErr != nil && !IsNotFound(runErr) {
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

	restate.Set(ctx, drivers.StateKey, NATGatewayState{Status: types.StatusDeleted})
	return nil
}

func (d *NATGatewayDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[NATGatewayState](ctx, drivers.StateKey)
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

	natGatewayID := state.Outputs.NatGatewayId
	if natGatewayID == "" {
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
		obs, runErr := api.DescribeNATGateway(rc, natGatewayID)
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
			state.Error = fmt.Sprintf("NAT gateway %s was deleted externally", natGatewayID)
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
	if IsFailed(observed.State) {
		state.Status = types.StatusError
		state.Error = failedStateError(observed).Error()
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: state.Error}, nil
	}

	drift := HasDrift(state.Desired, observed)
	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}

	if drift && state.Mode == types.ModeManaged {
		ctx.Log().Info("drift detected, correcting NAT gateway", "natGatewayId", natGatewayID)
		correctionErr := d.correctDrift(ctx, api, natGatewayID, state.Desired, observed)
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
		ctx.Log().Info("drift detected (observed mode, not correcting)", "natGatewayId", natGatewayID)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *NATGatewayDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[NATGatewayState](ctx, drivers.StateKey)
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

func (d *NATGatewayDriver) GetOutputs(ctx restate.ObjectSharedContext) (NATGatewayOutputs, error) {
	state, err := restate.Get[NATGatewayState](ctx, drivers.StateKey)
	if err != nil {
		return NATGatewayOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *NATGatewayDriver) correctDrift(ctx restate.ObjectContext, api NATGatewayAPI, natGatewayID string, desired NATGatewaySpec, observed ObservedState) error {
	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, natGatewayID, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}
	return nil
}

func (d *NATGatewayDriver) scheduleReconcile(ctx restate.ObjectContext, state *NATGatewayState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *NATGatewayDriver) waitUntilAvailable(ctx restate.ObjectContext, api NATGatewayAPI, natGatewayID string, spec NATGatewaySpec, state *NATGatewayState) error {
	_, waitErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if err := api.WaitUntilAvailable(rc, natGatewayID); err != nil {
			return restate.Void{}, err
		}
		return restate.Void{}, nil
	})
	if waitErr == nil {
		return nil
	}

	observed, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.DescribeNATGateway(rc, natGatewayID)
	})
	if descErr == nil && IsFailed(observed.State) {
		state.Observed = observed
		state.Outputs = outputsFromObserved(observed)
		state.Status = types.StatusError
		state.Error = failedStateError(observed).Error()
		restate.Set(ctx, drivers.StateKey, *state)
		return failedStateError(observed)
	}

	state.Status = types.StatusError
	state.Error = fmt.Sprintf("NAT gateway %s created but failed to reach available state: %v", natGatewayID, waitErr)
	state.Outputs = NATGatewayOutputs{NatGatewayId: natGatewayID, SubnetId: spec.SubnetId, ConnectivityType: spec.ConnectivityType, AllocationId: spec.AllocationId}
	restate.Set(ctx, drivers.StateKey, *state)
	return waitErr
}

func (d *NATGatewayDriver) deleteAndWait(ctx restate.ObjectContext, api NATGatewayAPI, natGatewayID string) error {
	_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteNATGateway(rc, natGatewayID)
		if runErr != nil && !IsNotFound(runErr) {
			return restate.Void{}, runErr
		}
		return restate.Void{}, nil
	})
	if err != nil {
		return err
	}
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.WaitUntilDeleted(rc, natGatewayID)
		if runErr != nil && !IsNotFound(runErr) {
			return restate.Void{}, runErr
		}
		return restate.Void{}, nil
	})
	return err
}

func (d *NATGatewayDriver) apiForAccount(ctx restate.ObjectContext, account string) (NATGatewayAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("NATGatewayDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve NAT gateway account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func specFromObserved(obs ObservedState) NATGatewaySpec {
	return applyDefaults(NATGatewaySpec{
		SubnetId:         obs.SubnetId,
		ConnectivityType: obs.ConnectivityType,
		AllocationId:     obs.AllocationId,
		Tags:             filterPraxisTags(obs.Tags),
	})
}

func outputsFromObserved(obs ObservedState) NATGatewayOutputs {
	return NATGatewayOutputs{
		NatGatewayId:       obs.NatGatewayId,
		SubnetId:           obs.SubnetId,
		VpcId:              obs.VpcId,
		ConnectivityType:   obs.ConnectivityType,
		State:              obs.State,
		PublicIp:           obs.PublicIp,
		PrivateIp:          obs.PrivateIp,
		AllocationId:       obs.AllocationId,
		NetworkInterfaceId: obs.NetworkInterfaceId,
	}
}

func defaultNATGatewayImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

func applyDefaults(spec NATGatewaySpec) NATGatewaySpec {
	if spec.ConnectivityType == "" {
		spec.ConnectivityType = "public"
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	return spec
}

func validateSpec(spec NATGatewaySpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.SubnetId == "" {
		return fmt.Errorf("subnetId is required")
	}
	if spec.ConnectivityType != "public" && spec.ConnectivityType != "private" {
		return fmt.Errorf("connectivityType must be \"public\" or \"private\"")
	}
	if spec.ConnectivityType == "public" && spec.AllocationId == "" {
		return fmt.Errorf("allocationId is required for public NAT gateways")
	}
	if spec.ConnectivityType == "private" && spec.AllocationId != "" {
		return fmt.Errorf("allocationId must be empty for private NAT gateways")
	}
	return nil
}

func failedStateError(observed ObservedState) error {
	msg := fmt.Sprintf("NAT gateway %s is in failed state", observed.NatGatewayId)
	if observed.FailureCode != "" {
		msg += fmt.Sprintf(" (%s)", observed.FailureCode)
	}
	if observed.FailureMessage != "" {
		msg += ": " + observed.FailureMessage
	}
	return fmt.Errorf("%s", msg)
}

func formatManagedKeyConflict(managedKey, natGatewayID string) error {
	return fmt.Errorf("NAT gateway name %q in this region is already managed by Praxis (natGatewayId: %s); remove the existing resource or use a different metadata.name", managedKey, natGatewayID)
}
