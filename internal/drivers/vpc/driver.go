package vpc

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

type VPCDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) VPCAPI
}

func NewVPCDriver(auth authservice.AuthClient) *VPCDriver {
	return NewVPCDriverWithFactory(auth, func(cfg aws.Config) VPCAPI {
		return NewVPCAPI(awsclient.NewEC2Client(cfg))
	})
}

func NewVPCDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) VPCAPI) *VPCDriver {
	if factory == nil {
		factory = func(cfg aws.Config) VPCAPI {
			return NewVPCAPI(awsclient.NewEC2Client(cfg))
		}
	}
	return &VPCDriver{auth: auth, apiFactory: factory}
}

func (d *VPCDriver) ServiceName() string {
	return ServiceName
}

func (d *VPCDriver) Provision(ctx restate.ObjectContext, spec VPCSpec) (VPCOutputs, error) {
	ctx.Log().Info("provisioning VPC", "name", spec.Tags["Name"], "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return VPCOutputs{}, restate.TerminalError(err, 400)
	}

	// --- Input validation ---
	if spec.CidrBlock == "" {
		return VPCOutputs{}, restate.TerminalError(fmt.Errorf("cidrBlock is required"), 400)
	}
	if spec.Region == "" {
		return VPCOutputs{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
	}
	if spec.EnableDnsHostnames && !spec.EnableDnsSupport {
		return VPCOutputs{}, restate.TerminalError(
			fmt.Errorf("enableDnsHostnames requires enableDnsSupport to be true"), 400,
		)
	}

	// --- Load current state ---
	state, err := restate.Get[VPCState](ctx, drivers.StateKey)
	if err != nil {
		return VPCOutputs{}, err
	}

	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	// --- Check if VPC already exists (re-provision path) ---
	vpcId := state.Outputs.VpcId
	if vpcId != "" {
		_, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, err := api.DescribeVpc(rc, vpcId)
			if err != nil {
				if IsNotFound(err) {
					return ObservedState{}, restate.TerminalError(err, 404)
				}
				return ObservedState{}, err
			}
			return obs, nil
		})
		if descErr != nil {
			vpcId = ""
		}
	}

	// --- Pre-flight ownership conflict check (first provision only) ---
	if vpcId == "" && spec.ManagedKey != "" {
		conflictId, conflictErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, spec.ManagedKey)
		})
		if conflictErr != nil {
			return VPCOutputs{}, conflictErr
		}
		if conflictId != "" {
			return VPCOutputs{}, restate.TerminalError(
				fmt.Errorf("VPC name %q in this region is already managed by Praxis (vpcId: %s); "+
					"remove the existing resource or use a different metadata.name", spec.ManagedKey, conflictId),
				409,
			)
		}
	}

	// --- Create VPC if it doesn't exist ---
	if vpcId == "" {
		newVpcId, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, err := api.CreateVpc(rc, spec)
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
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return VPCOutputs{}, err
		}
		vpcId = newVpcId

		// Wait for the VPC to reach "available" state.
		_, waitErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if err := api.WaitUntilAvailable(rc, vpcId); err != nil {
				return restate.Void{}, err
			}
			return restate.Void{}, nil
		})
		if waitErr != nil {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("VPC %s created but failed to reach available state: %v", vpcId, waitErr)
			state.Outputs = VPCOutputs{VpcId: vpcId}
			restate.Set(ctx, drivers.StateKey, state)
			return VPCOutputs{}, waitErr
		}

		// Apply DNS settings after VPC creation.
		if !spec.EnableDnsSupport {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.ModifyDnsSupport(rc, vpcId, false)
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = fmt.Sprintf("failed to disable DNS support: %v", err)
				state.Outputs = VPCOutputs{VpcId: vpcId}
				restate.Set(ctx, drivers.StateKey, state)
				return VPCOutputs{}, restate.TerminalError(err, 500)
			}
		}
		if spec.EnableDnsHostnames {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.ModifyDnsHostnames(rc, vpcId, true)
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = fmt.Sprintf("failed to enable DNS hostnames: %v", err)
				state.Outputs = VPCOutputs{VpcId: vpcId}
				restate.Set(ctx, drivers.StateKey, state)
				return VPCOutputs{}, restate.TerminalError(err, 500)
			}
		}
	} else {
		// --- Re-provision path: converge mutable attributes ---
		// DNS ordering: enable support before hostnames, disable hostnames before support.

		if spec.EnableDnsSupport && !state.Observed.EnableDnsSupport {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.ModifyDnsSupport(rc, vpcId, true)
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = fmt.Sprintf("failed to enable DNS support: %v", err)
				restate.Set(ctx, drivers.StateKey, state)
				return VPCOutputs{}, restate.TerminalError(err, 500)
			}
		}

		if spec.EnableDnsHostnames != state.Observed.EnableDnsHostnames {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.ModifyDnsHostnames(rc, vpcId, spec.EnableDnsHostnames)
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = fmt.Sprintf("failed to modify DNS hostnames: %v", err)
				restate.Set(ctx, drivers.StateKey, state)
				return VPCOutputs{}, restate.TerminalError(err, 500)
			}
		}

		if !spec.EnableDnsSupport && state.Observed.EnableDnsSupport {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.ModifyDnsSupport(rc, vpcId, false)
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = fmt.Sprintf("failed to disable DNS support: %v", err)
				restate.Set(ctx, drivers.StateKey, state)
				return VPCOutputs{}, restate.TerminalError(err, 500)
			}
		}

		if !tagsMatch(spec.Tags, state.Observed.Tags) {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.UpdateTags(rc, vpcId, spec.Tags)
			})
			if err != nil {
				state.Status = types.StatusError
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return VPCOutputs{}, restate.TerminalError(err, 500)
			}
		}
	}

	// --- Describe final state to populate outputs ---
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.DescribeVpc(rc, vpcId)
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = VPCOutputs{VpcId: vpcId}
		restate.Set(ctx, drivers.StateKey, state)
		return VPCOutputs{}, err
	}

	// --- Build outputs ---
	outputs := VPCOutputs{
		VpcId:              vpcId,
		CidrBlock:          observed.CidrBlock,
		State:              observed.State,
		EnableDnsHostnames: observed.EnableDnsHostnames,
		EnableDnsSupport:   observed.EnableDnsSupport,
		InstanceTenancy:    observed.InstanceTenancy,
		OwnerId:            observed.OwnerId,
		DhcpOptionsId:      observed.DhcpOptionsId,
		IsDefault:          observed.IsDefault,
		ARN:                fmt.Sprintf("arn:aws:ec2:%s:%s:vpc/%s", region, observed.OwnerId, vpcId),
	}

	// --- Commit state atomically ---
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

func (d *VPCDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (VPCOutputs, error) {
	ctx.Log().Info("importing VPC", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return VPCOutputs{}, restate.TerminalError(err, 400)
	}

	mode := defaultVPCImportMode(ref.Mode)

	state, err := restate.Get[VPCState](ctx, drivers.StateKey)
	if err != nil {
		return VPCOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, err := api.DescribeVpc(rc, ref.ResourceID)
		if err != nil {
			if IsNotFound(err) {
				return ObservedState{}, restate.TerminalError(
					fmt.Errorf("import failed: VPC %s does not exist", ref.ResourceID), 404,
				)
			}
			return ObservedState{}, err
		}
		return obs, nil
	})
	if err != nil {
		return VPCOutputs{}, err
	}

	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region

	outputs := VPCOutputs{
		VpcId:              observed.VpcId,
		CidrBlock:          observed.CidrBlock,
		State:              observed.State,
		EnableDnsHostnames: observed.EnableDnsHostnames,
		EnableDnsSupport:   observed.EnableDnsSupport,
		InstanceTenancy:    observed.InstanceTenancy,
		OwnerId:            observed.OwnerId,
		DhcpOptionsId:      observed.DhcpOptionsId,
		IsDefault:          observed.IsDefault,
		ARN:                fmt.Sprintf("arn:aws:ec2:%s:%s:vpc/%s", region, observed.OwnerId, observed.VpcId),
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

func specFromObserved(obs ObservedState) VPCSpec {
	return VPCSpec{
		CidrBlock:          obs.CidrBlock,
		EnableDnsHostnames: obs.EnableDnsHostnames,
		EnableDnsSupport:   obs.EnableDnsSupport,
		InstanceTenancy:    obs.InstanceTenancy,
		Tags:               obs.Tags,
	}
}

func defaultVPCImportMode(m types.Mode) types.Mode {
	if m == "" {
		return types.ModeObserved
	}
	return m
}

func (d *VPCDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting VPC", "key", restate.Key(ctx))

	state, err := restate.Get[VPCState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}

	if state.Mode == types.ModeObserved {
		return restate.TerminalError(
			fmt.Errorf("cannot delete VPC %s: resource is in Observed mode; "+
				"re-import with --mode managed to allow deletion", state.Outputs.VpcId),
			409,
		)
	}

	if state.Observed.IsDefault {
		return restate.TerminalError(
			fmt.Errorf("cannot delete VPC %s: it is the default VPC for this region; "+
				"default VPC deletion must be done manually via the AWS console", state.Outputs.VpcId),
			409,
		)
	}

	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	vpcId := state.Outputs.VpcId
	if vpcId == "" {
		restate.Set(ctx, drivers.StateKey, VPCState{Status: types.StatusDeleted})
		return nil
	}

	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if err := api.DeleteVpc(rc, vpcId); err != nil {
			if IsNotFound(err) {
				return restate.Void{}, nil
			}
			if IsDependencyViolation(err) {
				return restate.Void{}, restate.TerminalError(
					fmt.Errorf("cannot delete VPC %s: dependent resources exist (subnets, "+
						"internet gateways, NAT gateways, security groups, etc.); "+
						"remove all dependent resources first", vpcId),
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

	restate.Set(ctx, drivers.StateKey, VPCState{Status: types.StatusDeleted})
	return nil
}

func (d *VPCDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[VPCState](ctx, drivers.StateKey)
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

	vpcId := state.Outputs.VpcId
	if vpcId == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}

	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return types.ReconcileResult{}, err
	}

	type describeResult struct {
		Observed ObservedState `json:"observed"`
		Deleted  bool          `json:"deleted"`
	}

	describe, err := restate.Run(ctx, func(rc restate.RunContext) (describeResult, error) {
		obs, err := api.DescribeVpc(rc, vpcId)
		if err != nil {
			if IsNotFound(err) {
				return describeResult{Deleted: true}, nil
			}
			return describeResult{}, err
		}
		return describeResult{Observed: obs}, nil
	})
	if err != nil {
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}
	if describe.Deleted {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("VPC %s was deleted externally", vpcId)
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		d.reportDriftEvent(ctx, eventing.DriftEventExternalDelete, state.Error)
		return types.ReconcileResult{Error: state.Error}, nil
	}
	observed := describe.Observed

	state.Observed = observed
	state.LastReconcile = now

	drift := HasDrift(state.Desired, observed)

	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}

	if drift && state.Mode == types.ModeManaged {
		ctx.Log().Info("drift detected, correcting", "vpcId", vpcId)
		d.reportDriftEvent(ctx, eventing.DriftEventDetected, "")
		correctionErr := d.correctDrift(ctx, api, vpcId, state.Desired, observed)
		if correctionErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		d.reportDriftEvent(ctx, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}

	if drift && state.Mode == types.ModeObserved {
		ctx.Log().Info("drift detected (observed mode, not correcting)", "vpcId", vpcId)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		d.reportDriftEvent(ctx, eventing.DriftEventDetected, "")
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *VPCDriver) correctDrift(ctx restate.ObjectContext, api VPCAPI, vpcId string, desired VPCSpec, observed ObservedState) error {
	// DNS support must be corrected before DNS hostnames (dependency).
	if desired.EnableDnsSupport != observed.EnableDnsSupport {
		if desired.EnableDnsSupport {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.ModifyDnsSupport(rc, vpcId, desired.EnableDnsSupport)
			})
			if err != nil {
				return fmt.Errorf("modify DNS support: %w", err)
			}
		}
	}

	if desired.EnableDnsHostnames != observed.EnableDnsHostnames {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyDnsHostnames(rc, vpcId, desired.EnableDnsHostnames)
		})
		if err != nil {
			return fmt.Errorf("modify DNS hostnames: %w", err)
		}
	}

	if desired.EnableDnsSupport != observed.EnableDnsSupport {
		if !desired.EnableDnsSupport {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.ModifyDnsSupport(rc, vpcId, desired.EnableDnsSupport)
			})
			if err != nil {
				return fmt.Errorf("modify DNS support: %w", err)
			}
		}
	}

	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, vpcId, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}

	return nil
}

func (d *VPCDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[VPCState](ctx, drivers.StateKey)
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

func (d *VPCDriver) GetOutputs(ctx restate.ObjectSharedContext) (VPCOutputs, error) {
	state, err := restate.Get[VPCState](ctx, drivers.StateKey)
	if err != nil {
		return VPCOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *VPCDriver) reportDriftEvent(ctx restate.ObjectContext, eventType, errorMessage string) {
	restate.ServiceSend(ctx, eventing.ResourceEventBridgeServiceName, "ReportDrift").Send(eventing.DriftReportRequest{
		ResourceKey:  restate.Key(ctx),
		ResourceKind: ServiceName,
		EventType:    eventType,
		Error:        errorMessage,
	})
}

func (d *VPCDriver) scheduleReconcile(ctx restate.ObjectContext, state *VPCState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *VPCDriver) apiForAccount(ctx restate.ObjectContext, account string) (VPCAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("VPCDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve VPC account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}
