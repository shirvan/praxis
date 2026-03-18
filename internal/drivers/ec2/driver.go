package ec2

import (
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/internal/drivers"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

type EC2InstanceDriver struct {
	auth       *auth.Registry
	apiFactory func(aws.Config) EC2API
}

func NewEC2InstanceDriver(accounts *auth.Registry) *EC2InstanceDriver {
	return NewEC2InstanceDriverWithFactory(accounts, func(cfg aws.Config) EC2API {
		return NewEC2API(awsclient.NewEC2Client(cfg))
	})
}

func NewEC2InstanceDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) EC2API) *EC2InstanceDriver {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	if factory == nil {
		factory = func(cfg aws.Config) EC2API {
			return NewEC2API(awsclient.NewEC2Client(cfg))
		}
	}
	return &EC2InstanceDriver{auth: accounts, apiFactory: factory}
}

func (d *EC2InstanceDriver) ServiceName() string {
	return ServiceName
}

func (d *EC2InstanceDriver) Provision(ctx restate.ObjectContext, spec EC2InstanceSpec) (EC2InstanceOutputs, error) {
	ctx.Log().Info("provisioning EC2 instance", "name", spec.Tags["Name"], "key", restate.Key(ctx))
	api, _, err := d.apiForAccount(spec.Account)
	if err != nil {
		return EC2InstanceOutputs{}, restate.TerminalError(err, 400)
	}

	if spec.ImageId == "" {
		return EC2InstanceOutputs{}, restate.TerminalError(fmt.Errorf("imageId is required"), 400)
	}
	if spec.InstanceType == "" {
		return EC2InstanceOutputs{}, restate.TerminalError(fmt.Errorf("instanceType is required"), 400)
	}
	if spec.SubnetId == "" {
		return EC2InstanceOutputs{}, restate.TerminalError(fmt.Errorf("subnetId is required"), 400)
	}
	if spec.Region == "" {
		return EC2InstanceOutputs{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
	}

	state, err := restate.Get[EC2InstanceState](ctx, drivers.StateKey)
	if err != nil {
		return EC2InstanceOutputs{}, err
	}

	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	instanceId := state.Outputs.InstanceId
	if instanceId != "" {
		descResult, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, err := api.DescribeInstance(rc, instanceId)
			if err != nil {
				if IsNotFound(err) {
					return ObservedState{}, restate.TerminalError(err, 404)
				}
				return ObservedState{}, err
			}
			return obs, nil
		})
		if descErr != nil || descResult.State == "terminated" || descResult.State == "shutting-down" {
			instanceId = ""
		}
	}

	if instanceId == "" && spec.ManagedKey != "" {
		conflictId, conflictErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, err := api.FindByManagedKey(rc, spec.ManagedKey)
			if err != nil {
				if strings.Contains(err.Error(), "ownership corruption") {
					return "", restate.TerminalError(err, 500)
				}
				return "", err
			}
			return id, nil
		})
		if conflictErr != nil {
			state.Status = types.StatusError
			state.Error = conflictErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return EC2InstanceOutputs{}, conflictErr
		}
		if conflictId != "" {
			err := fmt.Errorf("instance name %q in this region is already managed by Praxis (instanceId: %s); remove the existing resource or use a different metadata.name", spec.ManagedKey, conflictId)
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return EC2InstanceOutputs{}, restate.TerminalError(err, 409)
		}
	}

	if instanceId == "" {
		newInstanceId, runErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, err := api.RunInstance(rc, spec)
			if err != nil {
				if IsInvalidParam(err) {
					return "", restate.TerminalError(err, 400)
				}
				if IsInsufficientCapacity(err) {
					return "", restate.TerminalError(err, 503)
				}
				return "", err
			}
			return id, nil
		})
		if runErr != nil {
			state.Status = types.StatusError
			state.Error = runErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return EC2InstanceOutputs{}, runErr
		}
		instanceId = newInstanceId

		_, waitErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if err := api.WaitUntilRunning(rc, instanceId); err != nil {
				return restate.Void{}, err
			}
			return restate.Void{}, nil
		})
		if waitErr != nil {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("instance %s created but failed to reach running state: %v", instanceId, waitErr)
			state.Outputs = EC2InstanceOutputs{InstanceId: instanceId}
			restate.Set(ctx, drivers.StateKey, state)
			return EC2InstanceOutputs{}, waitErr
		}
	} else {
		if spec.InstanceType != state.Observed.InstanceType && state.Observed.InstanceType != "" {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.ModifyInstanceType(rc, instanceId, spec.InstanceType)
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = fmt.Sprintf("failed to change instance type: %v", err)
				restate.Set(ctx, drivers.StateKey, state)
				return EC2InstanceOutputs{}, restate.TerminalError(err, 500)
			}
		}

		if !securityGroupsMatch(spec.SecurityGroupIds, state.Observed.SecurityGroupIds) && len(spec.SecurityGroupIds) > 0 {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.ModifySecurityGroups(rc, instanceId, spec.SecurityGroupIds)
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return EC2InstanceOutputs{}, restate.TerminalError(err, 500)
			}
		}

		if spec.Monitoring != state.Observed.Monitoring {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.UpdateMonitoring(rc, instanceId, spec.Monitoring)
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return EC2InstanceOutputs{}, restate.TerminalError(err, 500)
			}
		}

		if !tagsMatch(spec.Tags, state.Observed.Tags) {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.UpdateTags(rc, instanceId, spec.Tags)
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return EC2InstanceOutputs{}, restate.TerminalError(err, 500)
			}
		}
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, err := api.DescribeInstance(rc, instanceId)
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
		state.Outputs = EC2InstanceOutputs{InstanceId: instanceId}
		restate.Set(ctx, drivers.StateKey, state)
		return EC2InstanceOutputs{}, err
	}

	outputs := EC2InstanceOutputs{
		InstanceId:       instanceId,
		PrivateIpAddress: observed.PrivateIpAddress,
		PublicIpAddress:  observed.PublicIpAddress,
		PrivateDnsName:   observed.PrivateDnsName,
		ARN:              "",
		State:            observed.State,
		SubnetId:         observed.SubnetId,
		VpcId:            observed.VpcId,
	}

	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

func (d *EC2InstanceDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (EC2InstanceOutputs, error) {
	ctx.Log().Info("importing EC2 instance", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ref.Account)
	if err != nil {
		return EC2InstanceOutputs{}, restate.TerminalError(err, 400)
	}

	mode := defaultEC2ImportMode(ref.Mode)
	state, err := restate.Get[EC2InstanceState](ctx, drivers.StateKey)
	if err != nil {
		return EC2InstanceOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, err := api.DescribeInstance(rc, ref.ResourceID)
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
			return EC2InstanceOutputs{}, restate.TerminalError(fmt.Errorf("import failed: instance %s does not exist", ref.ResourceID), 404)
		}
		return EC2InstanceOutputs{}, err
	}
	if observed.State == "terminated" || observed.State == "shutting-down" {
		return EC2InstanceOutputs{}, restate.TerminalError(fmt.Errorf("import failed: instance %s is %s", ref.ResourceID, observed.State), 400)
	}

	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region

	outputs := EC2InstanceOutputs{
		InstanceId:       observed.InstanceId,
		PrivateIpAddress: observed.PrivateIpAddress,
		PublicIpAddress:  observed.PublicIpAddress,
		PrivateDnsName:   observed.PrivateDnsName,
		ARN:              "",
		State:            observed.State,
		SubnetId:         observed.SubnetId,
		VpcId:            observed.VpcId,
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

func (d *EC2InstanceDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting EC2 instance", "key", restate.Key(ctx))

	state, err := restate.Get[EC2InstanceState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete instance %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.InstanceId), 409)
	}

	api, _, err := d.apiForAccount(state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	instanceId := state.Outputs.InstanceId
	if instanceId == "" {
		restate.Set(ctx, drivers.StateKey, EC2InstanceState{Status: types.StatusDeleted})
		return nil
	}

	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if err := api.TerminateInstance(rc, instanceId); err != nil {
			if IsNotFound(err) {
				return restate.Void{}, nil
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

	restate.Set(ctx, drivers.StateKey, EC2InstanceState{Status: types.StatusDeleted})
	return nil
}

func (d *EC2InstanceDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[EC2InstanceState](ctx, drivers.StateKey)
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

	instanceId := state.Outputs.InstanceId
	if instanceId == "" {
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
		obs, err := api.DescribeInstance(rc, instanceId)
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
			state.Error = fmt.Sprintf("instance %s was terminated externally", instanceId)
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

	if observed.State == "terminated" || observed.State == "shutting-down" {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("instance %s is %s", instanceId, observed.State)
		state.Observed = observed
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: state.Error}, nil
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
		ctx.Log().Info("drift detected, correcting", "instanceId", instanceId)
		correctionErr := d.correctDrift(ctx, api, instanceId, state.Desired, observed)
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
		ctx.Log().Info("drift detected (observed mode, not correcting)", "instanceId", instanceId)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *EC2InstanceDriver) correctDrift(ctx restate.ObjectContext, api EC2API, instanceId string, desired EC2InstanceSpec, observed ObservedState) error {
	if desired.InstanceType != observed.InstanceType {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyInstanceType(rc, instanceId, desired.InstanceType)
		})
		if err != nil {
			return fmt.Errorf("modify instance type: %w", err)
		}
	}

	if !securityGroupsMatch(desired.SecurityGroupIds, observed.SecurityGroupIds) && len(desired.SecurityGroupIds) > 0 {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifySecurityGroups(rc, instanceId, desired.SecurityGroupIds)
		})
		if err != nil {
			return fmt.Errorf("modify security groups: %w", err)
		}
	}

	if desired.Monitoring != observed.Monitoring {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateMonitoring(rc, instanceId, desired.Monitoring)
		})
		if err != nil {
			return fmt.Errorf("update monitoring: %w", err)
		}
	}

	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, instanceId, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}

	return nil
}

func (d *EC2InstanceDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[EC2InstanceState](ctx, drivers.StateKey)
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

func (d *EC2InstanceDriver) GetOutputs(ctx restate.ObjectSharedContext) (EC2InstanceOutputs, error) {
	state, err := restate.Get[EC2InstanceState](ctx, drivers.StateKey)
	if err != nil {
		return EC2InstanceOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *EC2InstanceDriver) scheduleReconcile(ctx restate.ObjectContext, state *EC2InstanceState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *EC2InstanceDriver) apiForAccount(account string) (EC2API, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("EC2InstanceDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.Resolve(account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve EC2 account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func specFromObserved(obs ObservedState) EC2InstanceSpec {
	return EC2InstanceSpec{
		ImageId:            obs.ImageId,
		InstanceType:       obs.InstanceType,
		KeyName:            obs.KeyName,
		SubnetId:           obs.SubnetId,
		SecurityGroupIds:   obs.SecurityGroupIds,
		IamInstanceProfile: obs.IamInstanceProfile,
		Monitoring:         obs.Monitoring,
		Tags:               filterPraxisTags(obs.Tags),
	}
}

func defaultEC2ImportMode(m types.Mode) types.Mode {
	if m == "" {
		return types.ModeObserved
	}
	return m
}
