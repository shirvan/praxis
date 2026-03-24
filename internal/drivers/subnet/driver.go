package subnet

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type SubnetDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) SubnetAPI
}

func NewSubnetDriver(auth authservice.AuthClient) *SubnetDriver {
	return NewSubnetDriverWithFactory(auth, func(cfg aws.Config) SubnetAPI {
		return NewSubnetAPI(awsclient.NewEC2Client(cfg))
	})
}

func NewSubnetDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) SubnetAPI) *SubnetDriver {
	if factory == nil {
		factory = func(cfg aws.Config) SubnetAPI {
			return NewSubnetAPI(awsclient.NewEC2Client(cfg))
		}
	}
	return &SubnetDriver{auth: auth, apiFactory: factory}
}

func (d *SubnetDriver) ServiceName() string {
	return ServiceName
}

func (d *SubnetDriver) Provision(ctx restate.ObjectContext, spec SubnetSpec) (SubnetOutputs, error) {
	ctx.Log().Info("provisioning subnet", "name", spec.Tags["Name"], "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return SubnetOutputs{}, restate.TerminalError(err, 400)
	}

	if spec.Region == "" {
		return SubnetOutputs{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
	}
	if spec.VpcId == "" {
		return SubnetOutputs{}, restate.TerminalError(fmt.Errorf("vpcId is required"), 400)
	}
	if spec.CidrBlock == "" {
		return SubnetOutputs{}, restate.TerminalError(fmt.Errorf("cidrBlock is required"), 400)
	}
	if spec.AvailabilityZone == "" {
		return SubnetOutputs{}, restate.TerminalError(fmt.Errorf("availabilityZone is required"), 400)
	}

	state, err := restate.Get[SubnetState](ctx, drivers.StateKey)
	if err != nil {
		return SubnetOutputs{}, err
	}

	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	subnetId := state.Outputs.SubnetId
	if subnetId != "" {
		_, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, err := api.DescribeSubnet(rc, subnetId)
			if err != nil {
				if IsNotFound(err) {
					return ObservedState{}, restate.TerminalError(err, 404)
				}
				return ObservedState{}, err
			}
			return obs, nil
		})
		if descErr != nil {
			subnetId = ""
		}
	}

	if subnetId == "" && spec.ManagedKey != "" {
		conflictId, conflictErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, spec.ManagedKey)
		})
		if conflictErr != nil {
			state.Status = types.StatusError
			state.Error = conflictErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return SubnetOutputs{}, conflictErr
		}
		if conflictId != "" {
			err := fmt.Errorf("subnet name %q in VPC %s is already managed by Praxis (subnetId: %s); remove the existing resource or use a different metadata.name", spec.ManagedKey, spec.VpcId, conflictId)
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return SubnetOutputs{}, restate.TerminalError(err, 409)
		}
	}

	if subnetId == "" {
		newSubnetId, createErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, err := api.CreateSubnet(rc, spec)
			if err != nil {
				if IsInvalidParam(err) {
					return "", restate.TerminalError(err, 400)
				}
				if IsCidrConflict(err) {
					return "", restate.TerminalError(err, 409)
				}
				return "", err
			}
			return id, nil
		})
		if createErr != nil {
			state.Status = types.StatusError
			state.Error = createErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return SubnetOutputs{}, createErr
		}
		subnetId = newSubnetId

		_, waitErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if err := api.WaitUntilAvailable(rc, subnetId); err != nil {
				return restate.Void{}, err
			}
			return restate.Void{}, nil
		})
		if waitErr != nil {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("subnet %s created but failed to reach available state: %v", subnetId, waitErr)
			state.Outputs = SubnetOutputs{SubnetId: subnetId}
			restate.Set(ctx, drivers.StateKey, state)
			return SubnetOutputs{}, waitErr
		}

		if spec.MapPublicIpOnLaunch {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.ModifyMapPublicIp(rc, subnetId, true)
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = fmt.Sprintf("failed to enable mapPublicIpOnLaunch: %v", err)
				state.Outputs = SubnetOutputs{SubnetId: subnetId}
				restate.Set(ctx, drivers.StateKey, state)
				return SubnetOutputs{}, restate.TerminalError(err, 500)
			}
		}
	} else {
		if spec.MapPublicIpOnLaunch != state.Observed.MapPublicIpOnLaunch {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.ModifyMapPublicIp(rc, subnetId, spec.MapPublicIpOnLaunch)
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = fmt.Sprintf("failed to modify mapPublicIpOnLaunch: %v", err)
				restate.Set(ctx, drivers.StateKey, state)
				return SubnetOutputs{}, restate.TerminalError(err, 500)
			}
		}

		if !tagsMatch(spec.Tags, state.Observed.Tags) {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.UpdateTags(rc, subnetId, spec.Tags)
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return SubnetOutputs{}, restate.TerminalError(err, 500)
			}
		}
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, err := api.DescribeSubnet(rc, subnetId)
		if err != nil {
			if IsNotFound(err) {
				return ObservedState{}, restate.TerminalError(err, 404)
			}
			return ObservedState{}, err
		}
		return obs, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = SubnetOutputs{SubnetId: subnetId}
		restate.Set(ctx, drivers.StateKey, state)
		return SubnetOutputs{}, err
	}

	outputs := SubnetOutputs{
		SubnetId:            subnetId,
		ARN:                 fmt.Sprintf("arn:aws:ec2:%s:%s:subnet/%s", region, observed.OwnerId, subnetId),
		VpcId:               observed.VpcId,
		CidrBlock:           observed.CidrBlock,
		AvailabilityZone:    observed.AvailabilityZone,
		AvailabilityZoneId:  observed.AvailabilityZoneId,
		MapPublicIpOnLaunch: observed.MapPublicIpOnLaunch,
		State:               observed.State,
		OwnerId:             observed.OwnerId,
		AvailableIpCount:    observed.AvailableIpCount,
	}

	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

func (d *SubnetDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (SubnetOutputs, error) {
	ctx.Log().Info("importing subnet", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return SubnetOutputs{}, restate.TerminalError(err, 400)
	}

	mode := defaultSubnetImportMode(ref.Mode)
	state, err := restate.Get[SubnetState](ctx, drivers.StateKey)
	if err != nil {
		return SubnetOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, err := api.DescribeSubnet(rc, ref.ResourceID)
		if err != nil {
			if IsNotFound(err) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: subnet %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, err
		}
		return obs, nil
	})
	if err != nil {
		return SubnetOutputs{}, err
	}

	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region

	outputs := SubnetOutputs{
		SubnetId:            observed.SubnetId,
		ARN:                 fmt.Sprintf("arn:aws:ec2:%s:%s:subnet/%s", region, observed.OwnerId, observed.SubnetId),
		VpcId:               observed.VpcId,
		CidrBlock:           observed.CidrBlock,
		AvailabilityZone:    observed.AvailabilityZone,
		AvailabilityZoneId:  observed.AvailabilityZoneId,
		MapPublicIpOnLaunch: observed.MapPublicIpOnLaunch,
		State:               observed.State,
		OwnerId:             observed.OwnerId,
		AvailableIpCount:    observed.AvailableIpCount,
	}

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

func specFromObserved(obs ObservedState) SubnetSpec {
	return SubnetSpec{
		VpcId:               obs.VpcId,
		CidrBlock:           obs.CidrBlock,
		AvailabilityZone:    obs.AvailabilityZone,
		MapPublicIpOnLaunch: obs.MapPublicIpOnLaunch,
		Tags:                obs.Tags,
	}
}

func defaultSubnetImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

func (d *SubnetDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting subnet", "key", restate.Key(ctx))

	state, err := restate.Get[SubnetState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(
			fmt.Errorf("cannot delete subnet %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.SubnetId),
			409,
		)
	}

	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	subnetId := state.Outputs.SubnetId
	if subnetId == "" {
		restate.Set(ctx, drivers.StateKey, SubnetState{Status: types.StatusDeleted})
		return nil
	}

	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if err := api.DeleteSubnet(rc, subnetId); err != nil {
			if IsNotFound(err) {
				return restate.Void{}, nil
			}
			if IsDependencyViolation(err) {
				return restate.Void{}, restate.TerminalError(
					fmt.Errorf("cannot delete subnet %s: dependent resources exist in the subnet; remove instances, network interfaces, NAT gateways, or other attached resources first", subnetId),
					409,
				)
			}
			if IsInvalidParam(err) {
				return restate.Void{}, restate.TerminalError(err, 400)
			}
			return restate.Void{}, err
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}

	restate.Set(ctx, drivers.StateKey, SubnetState{Status: types.StatusDeleted})
	return nil
}

func (d *SubnetDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[SubnetState](ctx, drivers.StateKey)
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

	subnetId := state.Outputs.SubnetId
	if subnetId == "" {
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
		obs, err := api.DescribeSubnet(rc, subnetId)
		if err != nil {
			if IsNotFound(err) {
				return ObservedState{}, restate.TerminalError(err, 404)
			}
			return ObservedState{}, err
		}
		return obs, nil
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("subnet %s was deleted externally", subnetId)
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
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}

	if drift && state.Mode == types.ModeManaged {
		ctx.Log().Info("drift detected, correcting", "subnetId", subnetId)
		if correctionErr := d.correctDrift(ctx, api, subnetId, state.Desired, observed); correctionErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}

	if drift && state.Mode == types.ModeObserved {
		ctx.Log().Info("drift detected (observed mode, not correcting)", "subnetId", subnetId)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *SubnetDriver) correctDrift(ctx restate.ObjectContext, api SubnetAPI, subnetId string, desired SubnetSpec, observed ObservedState) error {
	if desired.MapPublicIpOnLaunch != observed.MapPublicIpOnLaunch {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyMapPublicIp(rc, subnetId, desired.MapPublicIpOnLaunch)
		})
		if err != nil {
			return fmt.Errorf("modify mapPublicIpOnLaunch: %w", err)
		}
	}

	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, subnetId, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}

	return nil
}

func (d *SubnetDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[SubnetState](ctx, drivers.StateKey)
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

func (d *SubnetDriver) GetOutputs(ctx restate.ObjectSharedContext) (SubnetOutputs, error) {
	state, err := restate.Get[SubnetState](ctx, drivers.StateKey)
	if err != nil {
		return SubnetOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *SubnetDriver) scheduleReconcile(ctx restate.ObjectContext, state *SubnetState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *SubnetDriver) apiForAccount(ctx restate.ObjectContext, account string) (SubnetAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("SubnetDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve Subnet account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}
