package vpcpeering

import (
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// VPCPeeringDriver is a Restate Virtual Object that manages VPC Peering Connection lifecycle.
type VPCPeeringDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) VPCPeeringAPI
}

// NewVPCPeeringDriver creates a production VPCPeeringDriver.
func NewVPCPeeringDriver(auth authservice.AuthClient) *VPCPeeringDriver {
	return NewVPCPeeringDriverWithFactory(auth, func(cfg aws.Config) VPCPeeringAPI {
		return NewVPCPeeringAPI(awsclient.NewEC2Client(cfg))
	})
}

// NewVPCPeeringDriverWithFactory allows tests to inject a custom VPCPeeringAPI factory.
func NewVPCPeeringDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) VPCPeeringAPI) *VPCPeeringDriver {
	if factory == nil {
		factory = func(cfg aws.Config) VPCPeeringAPI {
			return NewVPCPeeringAPI(awsclient.NewEC2Client(cfg))
		}
	}
	return &VPCPeeringDriver{auth: auth, apiFactory: factory}
}

func (d *VPCPeeringDriver) ServiceName() string {
	return ServiceName
}

// Provision implements idempotent create-or-converge for a VPC Peering Connection.
//
// Flow: validate → load state → ownership check → create if missing →
// auto-accept if configured (same-account only) → apply mutable settings
// (peering options + tags) → describe final state → commit state → schedule reconcile.
func (d *VPCPeeringDriver) Provision(ctx restate.ObjectContext, spec VPCPeeringSpec) (VPCPeeringOutputs, error) {
	ctx.Log().Info("provisioning VPC peering connection", "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return VPCPeeringOutputs{}, restate.TerminalError(err, 400)
	}
	if err := validateSpec(spec, region); err != nil {
		return VPCPeeringOutputs{}, restate.TerminalError(err, 400)
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}

	state, err := restate.Get[VPCPeeringState](ctx, drivers.StateKey)
	if err != nil {
		return VPCPeeringOutputs{}, err
	}

	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	peeringID := state.Outputs.VpcPeeringConnectionId
	currentObserved := state.Observed
	if peeringID != "" {
		described, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribeVPCPeeringConnection(rc, peeringID)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 404)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr != nil {
			peeringID = ""
			currentObserved = ObservedState{}
		} else {
			currentObserved = described
		}
	}

	if peeringID == "" && spec.ManagedKey != "" {
		conflictID, conflictErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, spec.ManagedKey)
		})
		if conflictErr != nil {
			state.Status = types.StatusError
			state.Error = conflictErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return VPCPeeringOutputs{}, conflictErr
		}
		if conflictID != "" {
			err := fmt.Errorf("VPC peering connection name %q in this region is already managed by Praxis (vpcPeeringConnectionId: %s); remove the existing resource or use a different metadata.name", spec.ManagedKey, conflictID)
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return VPCPeeringOutputs{}, restate.TerminalError(err, 409)
		}
	}

	created := false
	if peeringID == "" {
		createdID, createErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, runErr := api.CreateVPCPeeringConnection(rc, spec)
			if runErr != nil {
				switch {
				case IsVpcNotFound(runErr), IsInvalidParam(runErr):
					return "", restate.TerminalError(runErr, 400)
				case IsAlreadyExists(runErr), IsCidrOverlap(runErr), IsPeeringLimitExceeded(runErr):
					return "", restate.TerminalError(runErr, 409)
				default:
					return "", runErr
				}
			}
			return id, nil
		})
		if createErr != nil {
			state.Status = types.StatusError
			state.Error = createErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return VPCPeeringOutputs{}, createErr
		}
		peeringID = createdID
		created = true
	}

	if spec.AutoAccept {
		if currentObserved.Status == "" || currentObserved.Status == "pending-acceptance" || created {
			accepted, acceptErr := d.acceptIfPending(ctx, api, peeringID)
			if acceptErr != nil {
				state.Status = types.StatusError
				state.Error = acceptErr.Error()
				state.Outputs = VPCPeeringOutputs{VpcPeeringConnectionId: peeringID}
				restate.Set(ctx, drivers.StateKey, state)
				return VPCPeeringOutputs{}, acceptErr
			}
			if accepted {
				currentObserved.Status = "active"
			}
		}
	}

	if !created {
		if err := d.correctDrift(ctx, api, peeringID, spec, currentObserved); err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			state.Outputs = VPCPeeringOutputs{VpcPeeringConnectionId: peeringID}
			restate.Set(ctx, drivers.StateKey, state)
			return VPCPeeringOutputs{}, restate.TerminalError(err, 500)
		}
	} else if spec.AutoAccept {
		if err := d.applyMutableSettings(ctx, api, peeringID, spec, ObservedState{Status: "active"}); err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			state.Outputs = VPCPeeringOutputs{VpcPeeringConnectionId: peeringID}
			restate.Set(ctx, drivers.StateKey, state)
			return VPCPeeringOutputs{}, restate.TerminalError(err, 500)
		}
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeVPCPeeringConnection(rc, peeringID)
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
		state.Outputs = VPCPeeringOutputs{VpcPeeringConnectionId: peeringID}
		restate.Set(ctx, drivers.StateKey, state)
		return VPCPeeringOutputs{}, err
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

// Import captures an existing VPC Peering Connection's live state as the baseline.
func (d *VPCPeeringDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (VPCPeeringOutputs, error) {
	ctx.Log().Info("importing VPC peering connection", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return VPCPeeringOutputs{}, restate.TerminalError(err, 400)
	}

	mode := defaultImportMode(ref.Mode)
	state, err := restate.Get[VPCPeeringState](ctx, drivers.StateKey)
	if err != nil {
		return VPCPeeringOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeVPCPeeringConnection(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: VPC peering connection %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return VPCPeeringOutputs{}, err
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

// Delete removes the VPC Peering Connection.
func (d *VPCPeeringDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting VPC peering connection", "key", restate.Key(ctx))
	state, err := restate.Get[VPCPeeringState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete VPC peering connection %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.VpcPeeringConnectionId), 409)
	}

	peeringID := state.Outputs.VpcPeeringConnectionId
	if peeringID == "" {
		restate.Set(ctx, drivers.StateKey, VPCPeeringState{Status: types.StatusDeleted})
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
		runErr := api.DeleteVPCPeeringConnection(rc, peeringID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
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

	restate.Set(ctx, drivers.StateKey, VPCPeeringState{Status: types.StatusDeleted})
	return nil
}

// Reconcile checks actual state against desired and corrects drift (Managed)
// or reports it (Observed). If the peering is still pending-acceptance,
// Reconcile re-attempts auto-accept.
func (d *VPCPeeringDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[VPCPeeringState](ctx, drivers.StateKey)
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

	peeringID := state.Outputs.VpcPeeringConnectionId
	if peeringID == "" {
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
		obs, runErr := api.DescribeVPCPeeringConnection(rc, peeringID)
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
			state.Error = fmt.Sprintf("VPC peering connection %s was deleted externally", peeringID)
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
	state.Outputs = outputsFromObserved(observed)
	state.LastReconcile = now

	switch observed.Status {
	case "rejected", "expired", "deleted", "deleting", "failed":
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("VPC peering connection %s is in terminal provider state %q", peeringID, observed.Status)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: state.Error}, nil
	}

	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: HasDrift(state.Desired, observed), Correcting: false}, nil
	}

	if observed.Status == "pending-acceptance" && state.Desired.AutoAccept && state.Mode == types.ModeManaged {
		ctx.Log().Info("re-attempting VPC peering acceptance", "vpcPeeringConnectionId", peeringID)
		accepted, acceptErr := d.acceptIfPending(ctx, api, peeringID)
		if acceptErr != nil {
			state.Error = acceptErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: acceptErr.Error()}, nil
		}
		if accepted {
			observed, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
				return api.DescribeVPCPeeringConnection(rc, peeringID)
			})
			if err != nil {
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				d.scheduleReconcile(ctx, &state)
				return types.ReconcileResult{Drift: true, Correcting: true, Error: err.Error()}, nil
			}
			state.Observed = observed
			state.Outputs = outputsFromObserved(observed)
		}
	}

	drift := HasDrift(state.Desired, observed)
	if drift && state.Mode == types.ModeManaged {
		ctx.Log().Info("drift detected, correcting VPC peering connection", "vpcPeeringConnectionId", peeringID)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		correctionErr := d.correctDrift(ctx, api, peeringID, state.Desired, observed)
		if correctionErr != nil {
			state.Error = correctionErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}

	if drift && state.Mode == types.ModeObserved {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *VPCPeeringDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[VPCPeeringState](ctx, drivers.StateKey)
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

func (d *VPCPeeringDriver) GetOutputs(ctx restate.ObjectSharedContext) (VPCPeeringOutputs, error) {
	state, err := restate.Get[VPCPeeringState](ctx, drivers.StateKey)
	if err != nil {
		return VPCPeeringOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *VPCPeeringDriver) GetInputs(ctx restate.ObjectSharedContext) (VPCPeeringSpec, error) {
	state, err := restate.Get[VPCPeeringState](ctx, drivers.StateKey)
	if err != nil {
		return VPCPeeringSpec{}, err
	}
	return state.Desired, nil
}

// correctDrift delegates to applyMutableSettings for tag and option fixes.
func (d *VPCPeeringDriver) correctDrift(ctx restate.ObjectContext, api VPCPeeringAPI, peeringID string, desired VPCPeeringSpec, observed ObservedState) error {
	return d.applyMutableSettings(ctx, api, peeringID, desired, observed)
}

// applyMutableSettings updates peering options (DNS resolution) and tags
// to match the desired spec.
func (d *VPCPeeringDriver) applyMutableSettings(ctx restate.ObjectContext, api VPCPeeringAPI, peeringID string, desired VPCPeeringSpec, observed ObservedState) error {
	if observed.Status == "active" && (optionsDrift(desired.RequesterOptions, observed.RequesterOptions) || optionsDrift(desired.AccepterOptions, observed.AccepterOptions)) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyPeeringOptions(rc, peeringID, desired.RequesterOptions, desired.AccepterOptions)
		})
		if err != nil {
			return fmt.Errorf("modify peering options: %w", err)
		}
	}

	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, peeringID, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}

	return nil
}

// acceptIfPending attempts to accept the peering if it is in
// pending-acceptance state. Returns true if acceptance was attempted.
func (d *VPCPeeringDriver) acceptIfPending(ctx restate.ObjectContext, api VPCPeeringAPI, peeringID string) (bool, error) {
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.DescribeVPCPeeringConnection(rc, peeringID)
	})
	if err != nil {
		return false, err
	}
	if observed.Status != "pending-acceptance" {
		return false, nil
	}
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.AcceptVPCPeeringConnection(rc, peeringID)
		if runErr != nil {
			if IsInvalidParam(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
			}
			return restate.Void{}, runErr
		}
		return restate.Void{}, nil
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (d *VPCPeeringDriver) scheduleReconcile(ctx restate.ObjectContext, state *VPCPeeringState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *VPCPeeringDriver) apiForAccount(ctx restate.ObjectContext, account string) (VPCPeeringAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("VPCPeeringDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve VPC peering account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

// validateSpec checks that the peering spec is valid. Currently blocks
// cross-account (PeerOwnerId) and cross-region (PeerRegion) peering
// connections, which require additional coordination.
func validateSpec(spec VPCPeeringSpec, resolvedRegion string) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.RequesterVpcId) == "" {
		return fmt.Errorf("requesterVpcId is required")
	}
	if strings.TrimSpace(spec.AccepterVpcId) == "" {
		return fmt.Errorf("accepterVpcId is required")
	}
	if spec.RequesterVpcId == spec.AccepterVpcId {
		return fmt.Errorf("requesterVpcId and accepterVpcId must be different")
	}
	if spec.Region != resolvedRegion {
		return fmt.Errorf("spec.region %q does not match resolved account region %q", spec.Region, resolvedRegion)
	}
	if spec.PeerOwnerId != "" {
		return fmt.Errorf("cross-account VPC peering is not supported yet")
	}
	if spec.PeerRegion != "" && spec.PeerRegion != spec.Region {
		return fmt.Errorf("cross-region VPC peering is not supported yet")
	}
	return nil
}

func specFromObserved(obs ObservedState) VPCPeeringSpec {
	return VPCPeeringSpec{
		RequesterVpcId:   obs.RequesterVpcId,
		AccepterVpcId:    obs.AccepterVpcId,
		AutoAccept:       obs.Status == "active" || obs.Status == "pending-acceptance",
		RequesterOptions: cloneOptions(obs.RequesterOptions),
		AccepterOptions:  cloneOptions(obs.AccepterOptions),
		Tags:             filterPraxisTags(obs.Tags),
	}
}

func outputsFromObserved(obs ObservedState) VPCPeeringOutputs {
	return VPCPeeringOutputs{
		VpcPeeringConnectionId: obs.VpcPeeringConnectionId,
		RequesterVpcId:         obs.RequesterVpcId,
		AccepterVpcId:          obs.AccepterVpcId,
		RequesterCidrBlock:     obs.RequesterCidrBlock,
		AccepterCidrBlock:      obs.AccepterCidrBlock,
		Status:                 obs.Status,
		RequesterOwnerId:       obs.RequesterOwnerId,
		AccepterOwnerId:        obs.AccepterOwnerId,
	}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

func cloneOptions(options *PeeringOptions) *PeeringOptions {
	if options == nil {
		return nil
	}
	clone := *options
	return &clone
}
