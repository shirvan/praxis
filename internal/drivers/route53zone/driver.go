package route53zone

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

type HostedZoneDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) HostedZoneAPI
}

func NewHostedZoneDriver(auth authservice.AuthClient) *HostedZoneDriver {
	return NewHostedZoneDriverWithFactory(auth, func(cfg aws.Config) HostedZoneAPI {
		return NewHostedZoneAPI(awsclient.NewRoute53Client(cfg))
	})
}

func NewHostedZoneDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) HostedZoneAPI) *HostedZoneDriver {
	if factory == nil {
		factory = func(cfg aws.Config) HostedZoneAPI { return NewHostedZoneAPI(awsclient.NewRoute53Client(cfg)) }
	}
	return &HostedZoneDriver{auth: auth, apiFactory: factory}
}

func (d *HostedZoneDriver) ServiceName() string {
	return ServiceName
}

func (d *HostedZoneDriver) Provision(ctx restate.ObjectContext, spec HostedZoneSpec) (HostedZoneOutputs, error) {
	ctx.Log().Info("provisioning route53 hosted zone", "key", restate.Key(ctx))
	api, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return HostedZoneOutputs{}, restate.TerminalError(err, 400)
	}
	spec.Name = restate.Key(ctx)
	spec.ManagedKey = restate.Key(ctx)
	spec, err = normalizeHostedZoneSpec(spec)
	if err != nil {
		return HostedZoneOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[HostedZoneState](ctx, drivers.StateKey)
	if err != nil {
		return HostedZoneOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	hostedZoneID := state.Outputs.HostedZoneId
	observed := state.Observed
	if hostedZoneID != "" {
		described, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.DescribeHostedZone(rc, hostedZoneID)
		})
		if descErr == nil {
			observed = described
		} else {
			hostedZoneID = ""
		}
	}

	if hostedZoneID == "" {
		foundID, findErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindHostedZoneByName(rc, spec.Name)
		})
		if findErr != nil {
			state.Status = types.StatusError
			state.Error = findErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return HostedZoneOutputs{}, findErr
		}
		hostedZoneID = foundID
		if hostedZoneID != "" {
			described, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
				return api.DescribeHostedZone(rc, hostedZoneID)
			})
			if descErr != nil {
				state.Status = types.StatusError
				state.Error = descErr.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return HostedZoneOutputs{}, descErr
			}
			observed = described
		}
	}

	if hostedZoneID == "" {
		createdID, createErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, runErr := api.CreateHostedZone(rc, spec)
			if runErr != nil {
				if IsAlreadyExists(runErr) || IsConflict(runErr) {
					return "", restate.TerminalError(runErr, 409)
				}
				if IsInvalidInput(runErr) {
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
			return HostedZoneOutputs{}, createErr
		}
		hostedZoneID = createdID
	}

	if observed.HostedZoneId != "" && observed.IsPrivate != spec.IsPrivate {
		err := fmt.Errorf("hosted zone %s already exists with isPrivate=%t; requested isPrivate=%t cannot be changed", spec.Name, observed.IsPrivate, spec.IsPrivate)
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return HostedZoneOutputs{}, restate.TerminalError(err, 409)
	}

	if correctionErr := d.correctDrift(ctx, api, hostedZoneID, spec, observed); correctionErr != nil {
		state.Status = types.StatusError
		state.Error = correctionErr.Error()
		state.Outputs = HostedZoneOutputs{HostedZoneId: hostedZoneID, Name: spec.Name, IsPrivate: spec.IsPrivate}
		restate.Set(ctx, drivers.StateKey, state)
		return HostedZoneOutputs{}, correctionErr
	}

	observed, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.DescribeHostedZone(rc, hostedZoneID)
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = HostedZoneOutputs{HostedZoneId: hostedZoneID, Name: spec.Name, IsPrivate: spec.IsPrivate}
		restate.Set(ctx, drivers.StateKey, state)
		return HostedZoneOutputs{}, err
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

func (d *HostedZoneDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (HostedZoneOutputs, error) {
	ctx.Log().Info("importing route53 hosted zone", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return HostedZoneOutputs{}, restate.TerminalError(err, 400)
	}
	mode := defaultHostedZoneImportMode(ref.Mode)
	state, err := restate.Get[HostedZoneState](ctx, drivers.StateKey)
	if err != nil {
		return HostedZoneOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeHostedZone(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: hosted zone %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return HostedZoneOutputs{}, err
	}

	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.ManagedKey = restate.Key(ctx)
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

func (d *HostedZoneDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting route53 hosted zone", "key", restate.Key(ctx))
	state, err := restate.Get[HostedZoneState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete hosted zone %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.HostedZoneId), 409)
	}
	hostedZoneID := state.Outputs.HostedZoneId
	if hostedZoneID == "" {
		restate.Set(ctx, drivers.StateKey, HostedZoneState{Status: types.StatusDeleted})
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
		runErr := api.DeleteHostedZone(rc, hostedZoneID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsNotEmpty(runErr) || IsConflict(runErr) {
				return restate.Void{}, restate.TerminalError(fmt.Errorf("hosted zone %s is not empty; delete DNS records before deleting the zone", hostedZoneID), 409)
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
	restate.Set(ctx, drivers.StateKey, HostedZoneState{Status: types.StatusDeleted})
	return nil
}

func (d *HostedZoneDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[HostedZoneState](ctx, drivers.StateKey)
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
	hostedZoneID := state.Outputs.HostedZoneId
	if hostedZoneID == "" {
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
		obs, runErr := api.DescribeHostedZone(rc, hostedZoneID)
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
			state.Error = fmt.Sprintf("hosted zone %s was deleted externally", hostedZoneID)
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
		if correctionErr := d.correctDrift(ctx, api, hostedZoneID, state.Desired, observed); correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{Drift: drift, Correcting: false}, nil
}

func (d *HostedZoneDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[HostedZoneState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *HostedZoneDriver) GetOutputs(ctx restate.ObjectSharedContext) (HostedZoneOutputs, error) {
	state, err := restate.Get[HostedZoneState](ctx, drivers.StateKey)
	if err != nil {
		return HostedZoneOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *HostedZoneDriver) correctDrift(ctx restate.ObjectContext, api HostedZoneAPI, hostedZoneID string, desired HostedZoneSpec, observed ObservedState) error {
	if observed.HostedZoneId == "" {
		observed = ObservedState{HostedZoneId: hostedZoneID}
	}
	if normalizeZoneComment(desired.Comment) != normalizeZoneComment(observed.Comment) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateComment(rc, hostedZoneID, desired.Comment)
		})
		if err != nil {
			return fmt.Errorf("update hosted zone comment: %w", err)
		}
	}
	if desired.IsPrivate {
		desiredSet := make(map[string]HostedZoneVPC, len(desired.VPCs))
		for _, vpc := range normalizeHostedZoneVPCs(desired.VPCs) {
			desiredSet[hostedZoneVPCKey(vpc)] = vpc
		}
		observedSet := make(map[string]HostedZoneVPC, len(observed.VPCs))
		for _, vpc := range normalizeHostedZoneVPCs(observed.VPCs) {
			observedSet[hostedZoneVPCKey(vpc)] = vpc
		}
		for key, vpc := range desiredSet {
			if _, ok := observedSet[key]; !ok {
				_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
					return restate.Void{}, api.AssociateVPC(rc, hostedZoneID, vpc)
				})
				if err != nil {
					return fmt.Errorf("associate hosted zone VPC %s: %w", key, err)
				}
			}
		}
		for key, vpc := range observedSet {
			if _, ok := desiredSet[key]; !ok {
				_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
					return restate.Void{}, api.DisassociateVPC(rc, hostedZoneID, vpc)
				})
				if err != nil {
					return fmt.Errorf("disassociate hosted zone VPC %s: %w", key, err)
				}
			}
		}
	}
	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, hostedZoneID, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update hosted zone tags: %w", err)
		}
	}
	return nil
}

func (d *HostedZoneDriver) scheduleReconcile(ctx restate.ObjectContext, state *HostedZoneState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *HostedZoneDriver) apiForAccount(ctx restate.ObjectContext, account string) (HostedZoneAPI, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, fmt.Errorf("HostedZoneDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Route53 hosted zone account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), nil
}

func specFromObserved(observed ObservedState) HostedZoneSpec {
	return HostedZoneSpec{
		Name:      observed.Name,
		Comment:   observed.Comment,
		IsPrivate: observed.IsPrivate,
		VPCs:      observed.VPCs,
		Tags:      filterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) HostedZoneOutputs {
	return HostedZoneOutputs{
		HostedZoneId: observed.HostedZoneId,
		Name:         observed.Name,
		NameServers:  observed.NameServers,
		IsPrivate:    observed.IsPrivate,
		RecordCount:  observed.RecordCount,
	}
}

func defaultHostedZoneImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}
