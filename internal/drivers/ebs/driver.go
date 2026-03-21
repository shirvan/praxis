package ebs

import (
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	stssdk "github.com/aws/aws-sdk-go-v2/service/sts"
	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/internal/drivers"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

type EBSVolumeDriver struct {
	auth       *auth.Registry
	apiFactory func(aws.Config) EBSAPI
}

func NewEBSVolumeDriver(accounts *auth.Registry) *EBSVolumeDriver {
	return NewEBSVolumeDriverWithFactory(accounts, func(cfg aws.Config) EBSAPI {
		return NewEBSAPI(awsclient.NewEC2Client(cfg))
	})
}

func NewEBSVolumeDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) EBSAPI) *EBSVolumeDriver {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	if factory == nil {
		factory = func(cfg aws.Config) EBSAPI {
			return NewEBSAPI(awsclient.NewEC2Client(cfg))
		}
	}
	return &EBSVolumeDriver{auth: accounts, apiFactory: factory}
}

func (d *EBSVolumeDriver) ServiceName() string {
	return ServiceName
}

func (d *EBSVolumeDriver) Provision(ctx restate.ObjectContext, spec EBSVolumeSpec) (EBSVolumeOutputs, error) {
	ctx.Log().Info("provisioning EBS volume", "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(spec.Account)
	if err != nil {
		return EBSVolumeOutputs{}, restate.TerminalError(err, 400)
	}
	accountID, err := d.accountIDForAccount(ctx, spec.Account)
	if err != nil {
		return EBSVolumeOutputs{}, err
	}

	spec = applyDefaults(spec)
	if spec.Region == "" {
		return EBSVolumeOutputs{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
	}
	if spec.AvailabilityZone == "" {
		return EBSVolumeOutputs{}, restate.TerminalError(fmt.Errorf("availabilityZone is required"), 400)
	}
	if spec.VolumeType == "" {
		return EBSVolumeOutputs{}, restate.TerminalError(fmt.Errorf("volumeType is required"), 400)
	}
	if spec.SizeGiB < 1 {
		return EBSVolumeOutputs{}, restate.TerminalError(fmt.Errorf("sizeGiB must be >= 1"), 400)
	}

	state, err := restate.Get[EBSVolumeState](ctx, drivers.StateKey)
	if err != nil {
		return EBSVolumeOutputs{}, err
	}

	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	volumeID := state.Outputs.VolumeId
	if volumeID != "" {
		descResult, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribeVolume(rc, volumeID)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 404)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr != nil || descResult.State == "deleted" {
			volumeID = ""
		}
	}

	if volumeID == "" && spec.ManagedKey != "" {
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
			return EBSVolumeOutputs{}, conflictErr
		}
		if conflictID != "" {
			err := formatManagedKeyConflict(spec.ManagedKey, conflictID)
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return EBSVolumeOutputs{}, restate.TerminalError(err, 409)
		}
	}

	if volumeID == "" {
		newVolumeID, runErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, createErr := api.CreateVolume(rc, spec)
			if createErr != nil {
				if IsInvalidParam(createErr) {
					return "", restate.TerminalError(createErr, 400)
				}
				return "", createErr
			}
			return id, nil
		})
		if runErr != nil {
			state.Status = types.StatusError
			state.Error = runErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return EBSVolumeOutputs{}, runErr
		}
		volumeID = newVolumeID

		_, waitErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.WaitUntilAvailable(rc, volumeID); runErr != nil {
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if waitErr != nil {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("volume %s created but failed to reach available state: %v", volumeID, waitErr)
			state.Outputs = EBSVolumeOutputs{VolumeId: volumeID}
			restate.Set(ctx, drivers.StateKey, state)
			return EBSVolumeOutputs{}, waitErr
		}
	} else {
		if volumeNeedsModification(spec, state.Observed) {
			modifySpec := modificationSpec(spec, state.Observed)
			_, modifyErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				runErr := api.ModifyVolume(rc, volumeID, modifySpec)
				if runErr != nil {
					if IsModificationCooldown(runErr) {
						return restate.Void{}, restate.TerminalError(runErr, 429)
					}
					if IsInvalidParam(runErr) {
						return restate.Void{}, restate.TerminalError(runErr, 400)
					}
					return restate.Void{}, runErr
				}
				return restate.Void{}, nil
			})
			if modifyErr != nil {
				state.Status = types.StatusError
				state.Error = modifyErr.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return EBSVolumeOutputs{}, modifyErr
			}
		}

		if !tagsMatch(spec.Tags, state.Observed.Tags) {
			_, tagErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.UpdateTags(rc, volumeID, spec.Tags)
			})
			if tagErr != nil {
				state.Status = types.StatusError
				state.Error = tagErr.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return EBSVolumeOutputs{}, restate.TerminalError(tagErr, 500)
			}
		}
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeVolume(rc, volumeID)
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
		state.Outputs = EBSVolumeOutputs{VolumeId: volumeID}
		restate.Set(ctx, drivers.StateKey, state)
		return EBSVolumeOutputs{}, err
	}

	outputs := outputsFromObserved(observed, region, accountID)
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

func (d *EBSVolumeDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (EBSVolumeOutputs, error) {
	ctx.Log().Info("importing EBS volume", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ref.Account)
	if err != nil {
		return EBSVolumeOutputs{}, restate.TerminalError(err, 400)
	}
	accountID, err := d.accountIDForAccount(ctx, ref.Account)
	if err != nil {
		return EBSVolumeOutputs{}, err
	}

	mode := defaultEBSImportMode(ref.Mode)
	state, err := restate.Get[EBSVolumeState](ctx, drivers.StateKey)
	if err != nil {
		return EBSVolumeOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeVolume(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: volume %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return EBSVolumeOutputs{}, err
	}

	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region

	outputs := outputsFromObserved(observed, region, accountID)
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

func (d *EBSVolumeDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting EBS volume", "key", restate.Key(ctx))
	state, err := restate.Get[EBSVolumeState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete volume %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.VolumeId), 409)
	}

	volumeID := state.Outputs.VolumeId
	if volumeID == "" {
		state.Status = types.StatusDeleted
		state.Error = ""
		restate.Set(ctx, drivers.StateKey, state)
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
		runErr := api.DeleteVolume(rc, volumeID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsVolumeInUse(runErr) {
				return restate.Void{}, restate.TerminalError(fmt.Errorf("cannot delete volume %s: the volume is attached to an instance; detach it before deleting", volumeID), 409)
			}
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

	restate.Set(ctx, drivers.StateKey, EBSVolumeState{Status: types.StatusDeleted})
	return nil
}

func (d *EBSVolumeDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[EBSVolumeState](ctx, drivers.StateKey)
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

	volumeID := state.Outputs.VolumeId
	if volumeID == "" {
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
		obs, runErr := api.DescribeVolume(rc, volumeID)
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
			state.Error = fmt.Sprintf("volume %s was deleted externally", volumeID)
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
		ctx.Log().Info("drift detected, correcting", "volumeId", volumeID)
		correctionErr := d.correctDrift(ctx, api, volumeID, state.Desired, observed)
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
		ctx.Log().Info("drift detected (observed mode, not correcting)", "volumeId", volumeID)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *EBSVolumeDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[EBSVolumeState](ctx, drivers.StateKey)
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

func (d *EBSVolumeDriver) GetOutputs(ctx restate.ObjectSharedContext) (EBSVolumeOutputs, error) {
	state, err := restate.Get[EBSVolumeState](ctx, drivers.StateKey)
	if err != nil {
		return EBSVolumeOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *EBSVolumeDriver) correctDrift(ctx restate.ObjectContext, api EBSAPI, volumeID string, desired EBSVolumeSpec, observed ObservedState) error {
	if volumeNeedsModification(desired, observed) {
		modifySpec := modificationSpec(desired, observed)
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.ModifyVolume(rc, volumeID, modifySpec)
			if runErr != nil {
				if IsModificationCooldown(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 429)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("modify volume: %w", err)
		}
	}

	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, volumeID, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}

	return nil
}

func (d *EBSVolumeDriver) scheduleReconcile(ctx restate.ObjectContext, state *EBSVolumeState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *EBSVolumeDriver) apiForAccount(account string) (EBSAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("EBSVolumeDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.Resolve(account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve EBS account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func specFromObserved(obs ObservedState) EBSVolumeSpec {
	return EBSVolumeSpec{
		AvailabilityZone: obs.AvailabilityZone,
		VolumeType:       obs.VolumeType,
		SizeGiB:          obs.SizeGiB,
		Iops:             obs.Iops,
		Throughput:       obs.Throughput,
		Encrypted:        obs.Encrypted,
		KmsKeyId:         obs.KmsKeyId,
		SnapshotId:       obs.SnapshotId,
		Tags:             filterPraxisTags(obs.Tags),
	}
}

func outputsFromObserved(obs ObservedState, region, accountID string) EBSVolumeOutputs {
	arn := ""
	if region != "" && accountID != "" && obs.VolumeId != "" {
		arn = fmt.Sprintf("arn:aws:ec2:%s:%s:volume/%s", region, accountID, obs.VolumeId)
	}
	return EBSVolumeOutputs{
		VolumeId:         obs.VolumeId,
		ARN:              arn,
		AvailabilityZone: obs.AvailabilityZone,
		State:            obs.State,
		SizeGiB:          obs.SizeGiB,
		VolumeType:       obs.VolumeType,
		Encrypted:        obs.Encrypted,
	}
}

func defaultEBSImportMode(m types.Mode) types.Mode {
	if m == "" {
		return types.ModeObserved
	}
	return m
}

func (d *EBSVolumeDriver) accountIDForAccount(ctx restate.Context, account string) (string, error) {
	if d == nil || d.auth == nil {
		return "", restate.TerminalError(fmt.Errorf("EBSVolumeDriver is not configured with an auth registry"), 500)
	}
	awsCfg, err := d.auth.Resolve(account)
	if err != nil {
		return "", restate.TerminalError(fmt.Errorf("resolve EBS account %q: %w", account, err), 400)
	}
	accountID, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		out, runErr := stssdk.NewFromConfig(awsCfg).GetCallerIdentity(rc, &stssdk.GetCallerIdentityInput{})
		if runErr != nil {
			return "", runErr
		}
		return aws.ToString(out.Account), nil
	})
	if err != nil {
		return "", err
	}
	return accountID, nil
}

func applyDefaults(spec EBSVolumeSpec) EBSVolumeSpec {
	if spec.VolumeType == "" {
		spec.VolumeType = "gp3"
	}
	if spec.SizeGiB == 0 {
		spec.SizeGiB = 20
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	return spec
}

func volumeNeedsModification(desired EBSVolumeSpec, observed ObservedState) bool {
	if desired.VolumeType != observed.VolumeType {
		return true
	}
	if desired.SizeGiB > observed.SizeGiB {
		return true
	}
	if desired.Iops > 0 && desired.Iops != observed.Iops {
		return true
	}
	if desired.Throughput > 0 && desired.Throughput != observed.Throughput {
		return true
	}
	return false
}

func modificationSpec(desired EBSVolumeSpec, observed ObservedState) EBSVolumeSpec {
	copy := desired
	if copy.SizeGiB < observed.SizeGiB {
		copy.SizeGiB = observed.SizeGiB
	}
	return copy
}
