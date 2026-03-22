package igw

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/internal/drivers"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

type IGWDriver struct {
	auth       *auth.Registry
	apiFactory func(aws.Config) IGWAPI
}

func NewIGWDriver(accounts *auth.Registry) *IGWDriver {
	return NewIGWDriverWithFactory(accounts, func(cfg aws.Config) IGWAPI {
		return NewIGWAPI(awsclient.NewEC2Client(cfg))
	})
}

func NewIGWDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) IGWAPI) *IGWDriver {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	if factory == nil {
		factory = func(cfg aws.Config) IGWAPI {
			return NewIGWAPI(awsclient.NewEC2Client(cfg))
		}
	}
	return &IGWDriver{auth: accounts, apiFactory: factory}
}

func (d *IGWDriver) ServiceName() string {
	return ServiceName
}

func (d *IGWDriver) Provision(ctx restate.ObjectContext, spec IGWSpec) (IGWOutputs, error) {
	ctx.Log().Info("provisioning internet gateway", "key", restate.Key(ctx))
	api, _, err := d.apiForAccount(spec.Account)
	if err != nil {
		return IGWOutputs{}, restate.TerminalError(err, 400)
	}

	if spec.Region == "" {
		return IGWOutputs{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
	}
	if spec.VpcId == "" {
		return IGWOutputs{}, restate.TerminalError(fmt.Errorf("vpcId is required"), 400)
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}

	state, err := restate.Get[IGWState](ctx, drivers.StateKey)
	if err != nil {
		return IGWOutputs{}, err
	}

	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	internetGatewayID := state.Outputs.InternetGatewayId
	currentObserved := state.Observed
	if internetGatewayID != "" {
		described, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribeInternetGateway(rc, internetGatewayID)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 404)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr != nil {
			internetGatewayID = ""
			currentObserved = ObservedState{}
		} else {
			currentObserved = described
		}
	}

	if internetGatewayID == "" && spec.ManagedKey != "" {
		conflictID, conflictErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, spec.ManagedKey)
		})
		if conflictErr != nil {
			state.Status = types.StatusError
			state.Error = conflictErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return IGWOutputs{}, conflictErr
		}
		if conflictID != "" {
			err := formatManagedKeyConflict(spec.ManagedKey, conflictID)
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return IGWOutputs{}, restate.TerminalError(err, 409)
		}
	}

	created := false
	if internetGatewayID == "" {
		createdID, createErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, runErr := api.CreateInternetGateway(rc, spec)
			if runErr != nil {
				if IsInvalidParam(runErr) {
					return "", restate.TerminalError(runErr, 400)
				}
				return "", runErr
			}
			return id, nil
		})
		if createErr != nil {
			state.Status = types.StatusError
			state.Error = createErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return IGWOutputs{}, createErr
		}
		internetGatewayID = createdID
		created = true
	}

	if currentObserved.AttachedVpcId != "" && currentObserved.AttachedVpcId != spec.VpcId {
		ctx.Log().Info("changing internet gateway attachment", "internetGatewayId", internetGatewayID, "oldVpcId", currentObserved.AttachedVpcId, "newVpcId", spec.VpcId)
		_, detachErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.DetachFromVpc(rc, internetGatewayID, currentObserved.AttachedVpcId)
			if runErr != nil && !IsNotAttached(runErr) {
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if detachErr != nil {
			state.Status = types.StatusError
			state.Error = detachErr.Error()
			state.Outputs = IGWOutputs{InternetGatewayId: internetGatewayID, VpcId: currentObserved.AttachedVpcId}
			restate.Set(ctx, drivers.StateKey, state)
			return IGWOutputs{}, detachErr
		}
		currentObserved.AttachedVpcId = ""
	}

	if currentObserved.AttachedVpcId != spec.VpcId {
		_, attachErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.AttachToVpc(rc, internetGatewayID, spec.VpcId)
			if runErr != nil {
				if IsAlreadyAttached(runErr) {
					obs, descErr := api.DescribeInternetGateway(rc, internetGatewayID)
					if descErr == nil && obs.AttachedVpcId == spec.VpcId {
						return restate.Void{}, nil
					}
					return restate.Void{}, restate.TerminalError(
						fmt.Errorf("cannot attach internet gateway %s to VPC %s: the VPC already has an internet gateway attached", internetGatewayID, spec.VpcId),
						409,
					)
				}
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if attachErr != nil {
			state.Status = types.StatusError
			state.Error = attachErr.Error()
			state.Outputs = IGWOutputs{InternetGatewayId: internetGatewayID, VpcId: spec.VpcId}
			restate.Set(ctx, drivers.StateKey, state)
			return IGWOutputs{}, attachErr
		}
	}

	if !created && !tagsMatch(spec.Tags, currentObserved.Tags) {
		_, tagErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, internetGatewayID, spec.Tags)
		})
		if tagErr != nil {
			state.Status = types.StatusError
			state.Error = tagErr.Error()
			state.Outputs = IGWOutputs{InternetGatewayId: internetGatewayID, VpcId: spec.VpcId}
			restate.Set(ctx, drivers.StateKey, state)
			return IGWOutputs{}, restate.TerminalError(tagErr, 500)
		}
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeInternetGateway(rc, internetGatewayID)
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
		state.Outputs = IGWOutputs{InternetGatewayId: internetGatewayID, VpcId: spec.VpcId}
		restate.Set(ctx, drivers.StateKey, state)
		return IGWOutputs{}, err
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

func (d *IGWDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (IGWOutputs, error) {
	ctx.Log().Info("importing internet gateway", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ref.Account)
	if err != nil {
		return IGWOutputs{}, restate.TerminalError(err, 400)
	}

	mode := defaultIGWImportMode(ref.Mode)
	state, err := restate.Get[IGWState](ctx, drivers.StateKey)
	if err != nil {
		return IGWOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeInternetGateway(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(
					fmt.Errorf("import failed: internet gateway %s does not exist", ref.ResourceID),
					404,
				)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return IGWOutputs{}, err
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

func (d *IGWDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting internet gateway", "key", restate.Key(ctx))
	state, err := restate.Get[IGWState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(
			fmt.Errorf("cannot delete internet gateway %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.InternetGatewayId),
			409,
		)
	}

	internetGatewayID := state.Outputs.InternetGatewayId
	if internetGatewayID == "" {
		restate.Set(ctx, drivers.StateKey, IGWState{Status: types.StatusDeleted})
		return nil
	}

	api, _, err := d.apiForAccount(state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	currentObserved, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeInternetGateway(rc, internetGatewayID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, nil
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}

	if currentObserved.InternetGatewayId == "" {
		restate.Set(ctx, drivers.StateKey, IGWState{Status: types.StatusDeleted})
		return nil
	}

	if currentObserved.AttachedVpcId != "" {
		_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.DetachFromVpc(rc, internetGatewayID, currentObserved.AttachedVpcId)
			if runErr != nil && !IsNotAttached(runErr) {
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
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
	}

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteInternetGateway(rc, internetGatewayID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsDependencyViolation(runErr) {
				return restate.Void{}, restate.TerminalError(
					fmt.Errorf("cannot delete internet gateway %s: it is still attached or referenced by dependent resources", internetGatewayID),
					409,
				)
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

	restate.Set(ctx, drivers.StateKey, IGWState{Status: types.StatusDeleted})
	return nil
}

func (d *IGWDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[IGWState](ctx, drivers.StateKey)
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

	internetGatewayID := state.Outputs.InternetGatewayId
	if internetGatewayID == "" {
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
		obs, runErr := api.DescribeInternetGateway(rc, internetGatewayID)
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
			state.Error = fmt.Sprintf("internet gateway %s was deleted externally", internetGatewayID)
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
		ctx.Log().Info("drift detected, correcting internet gateway", "internetGatewayId", internetGatewayID)
		correctionErr := d.correctDrift(ctx, api, internetGatewayID, state.Desired, observed)
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
		ctx.Log().Info("drift detected (observed mode, not correcting)", "internetGatewayId", internetGatewayID)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *IGWDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[IGWState](ctx, drivers.StateKey)
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

func (d *IGWDriver) GetOutputs(ctx restate.ObjectSharedContext) (IGWOutputs, error) {
	state, err := restate.Get[IGWState](ctx, drivers.StateKey)
	if err != nil {
		return IGWOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *IGWDriver) correctDrift(ctx restate.ObjectContext, api IGWAPI, internetGatewayID string, desired IGWSpec, observed ObservedState) error {
	if desired.VpcId != observed.AttachedVpcId {
		if observed.AttachedVpcId != "" {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				runErr := api.DetachFromVpc(rc, internetGatewayID, observed.AttachedVpcId)
				if runErr != nil && !IsNotAttached(runErr) {
					return restate.Void{}, runErr
				}
				return restate.Void{}, nil
			})
			if err != nil {
				return fmt.Errorf("detach from VPC: %w", err)
			}
		}

		if desired.VpcId != "" {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				runErr := api.AttachToVpc(rc, internetGatewayID, desired.VpcId)
				if runErr != nil {
					if IsAlreadyAttached(runErr) {
						return restate.Void{}, fmt.Errorf("target VPC %s already has an internet gateway attached", desired.VpcId)
					}
					return restate.Void{}, runErr
				}
				return restate.Void{}, nil
			})
			if err != nil {
				return fmt.Errorf("attach to VPC: %w", err)
			}
		}
	}

	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, internetGatewayID, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}

	return nil
}

func (d *IGWDriver) scheduleReconcile(ctx restate.ObjectContext, state *IGWState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *IGWDriver) apiForAccount(account string) (IGWAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("IGWDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.Resolve(account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve internet gateway account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func specFromObserved(obs ObservedState) IGWSpec {
	return IGWSpec{
		VpcId: obs.AttachedVpcId,
		Tags:  filterPraxisTags(obs.Tags),
	}
}

func outputsFromObserved(obs ObservedState) IGWOutputs {
	state := "detached"
	if obs.AttachedVpcId != "" {
		state = "available"
	}
	return IGWOutputs{
		InternetGatewayId: obs.InternetGatewayId,
		VpcId:             obs.AttachedVpcId,
		OwnerId:           obs.OwnerId,
		State:             state,
	}
}

func defaultIGWImportMode(m types.Mode) types.Mode {
	if m == "" {
		return types.ModeObserved
	}
	return m
}

func formatManagedKeyConflict(managedKey, internetGatewayID string) error {
	return fmt.Errorf("internet gateway name %q in this region is already managed by Praxis (internetGatewayId: %s); remove the existing resource or use a different metadata.name", managedKey, internetGatewayID)
}
