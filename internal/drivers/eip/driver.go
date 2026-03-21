package eip

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

type ElasticIPDriver struct {
	auth       *auth.Registry
	apiFactory func(aws.Config) EIPAPI
}

func NewElasticIPDriver(accounts *auth.Registry) *ElasticIPDriver {
	return NewElasticIPDriverWithFactory(accounts, func(cfg aws.Config) EIPAPI {
		return NewEIPAPI(awsclient.NewEC2Client(cfg))
	})
}

func NewElasticIPDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) EIPAPI) *ElasticIPDriver {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	if factory == nil {
		factory = func(cfg aws.Config) EIPAPI {
			return NewEIPAPI(awsclient.NewEC2Client(cfg))
		}
	}
	return &ElasticIPDriver{auth: accounts, apiFactory: factory}
}

func (d *ElasticIPDriver) ServiceName() string {
	return ServiceName
}

func (d *ElasticIPDriver) Provision(ctx restate.ObjectContext, spec ElasticIPSpec) (ElasticIPOutputs, error) {
	ctx.Log().Info("provisioning elastic IP", "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(spec.Account)
	if err != nil {
		return ElasticIPOutputs{}, restate.TerminalError(err, 400)
	}
	accountID, err := d.accountIDForAccount(ctx, spec.Account)
	if err != nil {
		return ElasticIPOutputs{}, err
	}

	spec = applyDefaults(spec)
	if spec.Region == "" {
		return ElasticIPOutputs{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
	}
	if spec.Domain != "vpc" {
		return ElasticIPOutputs{}, restate.TerminalError(fmt.Errorf("domain must be \"vpc\""), 400)
	}

	state, err := restate.Get[ElasticIPState](ctx, drivers.StateKey)
	if err != nil {
		return ElasticIPOutputs{}, err
	}

	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	allocationID := state.Outputs.AllocationId
	if allocationID != "" {
		descResult, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribeAddress(rc, allocationID)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 404)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr != nil || descResult.AllocationId == "" {
			allocationID = ""
		}
	}

	if allocationID == "" && spec.ManagedKey != "" {
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
			return ElasticIPOutputs{}, conflictErr
		}
		if conflictID != "" {
			err := formatManagedKeyConflict(spec.ManagedKey, conflictID)
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return ElasticIPOutputs{}, restate.TerminalError(err, 409)
		}
	}

	if allocationID == "" {
		result, runErr := restate.Run(ctx, func(rc restate.RunContext) (ElasticIPOutputs, error) {
			allocID, publicIP, allocErr := api.AllocateAddress(rc, spec)
			if allocErr != nil {
				if IsAddressLimitExceeded(allocErr) {
					return ElasticIPOutputs{}, restate.TerminalError(allocErr, 503)
				}
				return ElasticIPOutputs{}, allocErr
			}
			return ElasticIPOutputs{AllocationId: allocID, PublicIp: publicIP}, nil
		})
		if runErr != nil {
			state.Status = types.StatusError
			state.Error = runErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return ElasticIPOutputs{}, runErr
		}
		allocationID = result.AllocationId
	} else if !tagsMatch(spec.Tags, state.Observed.Tags) {
		_, tagErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, allocationID, spec.Tags)
		})
		if tagErr != nil {
			state.Status = types.StatusError
			state.Error = tagErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return ElasticIPOutputs{}, tagErr
		}
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeAddress(rc, allocationID)
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
		state.Outputs = ElasticIPOutputs{AllocationId: allocationID}
		restate.Set(ctx, drivers.StateKey, state)
		return ElasticIPOutputs{}, err
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

func (d *ElasticIPDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (ElasticIPOutputs, error) {
	ctx.Log().Info("importing elastic IP", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ref.Account)
	if err != nil {
		return ElasticIPOutputs{}, restate.TerminalError(err, 400)
	}
	accountID, err := d.accountIDForAccount(ctx, ref.Account)
	if err != nil {
		return ElasticIPOutputs{}, err
	}

	mode := defaultEIPImportMode(ref.Mode)
	state, err := restate.Get[ElasticIPState](ctx, drivers.StateKey)
	if err != nil {
		return ElasticIPOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeAddress(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: elastic IP %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return ElasticIPOutputs{}, err
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

func (d *ElasticIPDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting elastic IP", "key", restate.Key(ctx))
	state, err := restate.Get[ElasticIPState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot release elastic IP %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.AllocationId), 409)
	}

	allocationID := state.Outputs.AllocationId
	if allocationID == "" {
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
		runErr := api.ReleaseAddress(rc, allocationID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsAssociationExists(runErr) {
				return restate.Void{}, restate.TerminalError(fmt.Errorf("elastic IP %s is still associated with an instance or network interface; disassociate it before releasing", allocationID), 409)
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

	restate.Set(ctx, drivers.StateKey, ElasticIPState{Status: types.StatusDeleted})
	return nil
}

func (d *ElasticIPDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[ElasticIPState](ctx, drivers.StateKey)
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

	allocationID := state.Outputs.AllocationId
	if allocationID == "" {
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
		obs, runErr := api.DescribeAddress(rc, allocationID)
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
			state.Error = fmt.Sprintf("elastic IP %s was deleted externally", allocationID)
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
		ctx.Log().Info("drift detected, correcting elastic IP", "allocationId", allocationID)
		correctionErr := d.correctDrift(ctx, api, allocationID, state.Desired, observed)
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
		ctx.Log().Info("drift detected (observed mode, not correcting)", "allocationId", allocationID)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *ElasticIPDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[ElasticIPState](ctx, drivers.StateKey)
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

func (d *ElasticIPDriver) GetOutputs(ctx restate.ObjectSharedContext) (ElasticIPOutputs, error) {
	state, err := restate.Get[ElasticIPState](ctx, drivers.StateKey)
	if err != nil {
		return ElasticIPOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *ElasticIPDriver) correctDrift(ctx restate.ObjectContext, api EIPAPI, allocationID string, desired ElasticIPSpec, observed ObservedState) error {
	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, allocationID, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}
	return nil
}

func (d *ElasticIPDriver) scheduleReconcile(ctx restate.ObjectContext, state *ElasticIPState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *ElasticIPDriver) apiForAccount(account string) (EIPAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("ElasticIPDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.Resolve(account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve Elastic IP account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func specFromObserved(obs ObservedState) ElasticIPSpec {
	return ElasticIPSpec{
		Domain:             obs.Domain,
		NetworkBorderGroup: obs.NetworkBorderGroup,
		Tags:               filterPraxisTags(obs.Tags),
	}
}

func outputsFromObserved(obs ObservedState, region, accountID string) ElasticIPOutputs {
	arn := ""
	if region != "" && accountID != "" && obs.AllocationId != "" {
		arn = fmt.Sprintf("arn:aws:ec2:%s:%s:elastic-ip/%s", region, accountID, obs.AllocationId)
	}
	return ElasticIPOutputs{
		AllocationId:       obs.AllocationId,
		PublicIp:           obs.PublicIp,
		ARN:                arn,
		Domain:             obs.Domain,
		NetworkBorderGroup: obs.NetworkBorderGroup,
	}
}

func defaultEIPImportMode(m types.Mode) types.Mode {
	if m == "" {
		return types.ModeObserved
	}
	return m
}

func (d *ElasticIPDriver) accountIDForAccount(ctx restate.Context, account string) (string, error) {
	if d == nil || d.auth == nil {
		return "", restate.TerminalError(fmt.Errorf("ElasticIPDriver is not configured with an auth registry"), 500)
	}
	awsCfg, err := d.auth.Resolve(account)
	if err != nil {
		return "", restate.TerminalError(fmt.Errorf("resolve Elastic IP account %q: %w", account, err), 400)
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

func applyDefaults(spec ElasticIPSpec) ElasticIPSpec {
	if spec.Domain == "" {
		spec.Domain = "vpc"
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	return spec
}

func formatManagedKeyConflict(managedKey, allocationID string) error {
	return fmt.Errorf("elastic IP name %q in this region is already managed by Praxis (allocationId: %s); remove the existing resource or use a different metadata.name", managedKey, allocationID)
}
