package dbsubnetgroup

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

// DBSubnetGroupDriver is a Restate Virtual Object that manages the lifecycle of
// AWS RDS DB Subnet Groups. Restate guarantees single-writer access per key.
type DBSubnetGroupDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) DBSubnetGroupAPI
}

// NewDBSubnetGroupDriver creates a driver backed by real AWS RDS clients.
func NewDBSubnetGroupDriver(auth authservice.AuthClient) *DBSubnetGroupDriver {
	return NewDBSubnetGroupDriverWithFactory(auth, func(cfg aws.Config) DBSubnetGroupAPI {
		return NewDBSubnetGroupAPI(awsclient.NewRDSClient(cfg))
	})
}

// NewDBSubnetGroupDriverWithFactory creates a driver with a custom API factory (for tests).
func NewDBSubnetGroupDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) DBSubnetGroupAPI) *DBSubnetGroupDriver {
	if factory == nil {
		factory = func(cfg aws.Config) DBSubnetGroupAPI { return NewDBSubnetGroupAPI(awsclient.NewRDSClient(cfg)) }
	}
	return &DBSubnetGroupDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate service name for registration.
func (d *DBSubnetGroupDriver) ServiceName() string {
	return ServiceName
}

// Provision creates or converges a DB Subnet Group to the desired spec.
// Idempotent: if the group exists, applies drift corrections for description, subnets, and tags.
func (d *DBSubnetGroupDriver) Provision(ctx restate.ObjectContext, spec DBSubnetGroupSpec) (DBSubnetGroupOutputs, error) {
	ctx.Log().Info("provisioning db subnet group", "key", restate.Key(ctx))
	api, _, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return DBSubnetGroupOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	if err := validateSpec(spec); err != nil {
		return DBSubnetGroupOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[DBSubnetGroupState](ctx, drivers.StateKey)
	if err != nil {
		return DBSubnetGroupOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	existing := state.Outputs.GroupName
	if existing == "" {
		existing = spec.GroupName
	}
	observed := state.Observed
	if existing != "" {
		described, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribeDBSubnetGroup(rc, existing)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 404)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr == nil {
			observed = described
		}
	}

	if observed.GroupName == "" {
		createdARN, createErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			arn, runErr := api.CreateDBSubnetGroup(rc, spec)
			if runErr != nil {
				if IsAlreadyExists(runErr) {
					return "", restate.TerminalError(runErr, 409)
				}
				if IsInvalidParam(runErr) {
					return "", restate.TerminalError(runErr, 400)
				}
				return "", runErr
			}
			return arn, nil
		})
		if createErr != nil {
			state.Status = types.StatusError
			state.Error = createErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return DBSubnetGroupOutputs{}, createErr
		}
		_ = createdARN
	} else {
		if correctionErr := d.correctDrift(ctx, api, spec, observed); correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			state.Outputs = outputsFromObserved(observed)
			restate.Set(ctx, drivers.StateKey, state)
			return DBSubnetGroupOutputs{}, correctionErr
		}
	}

	observed, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeDBSubnetGroup(rc, spec.GroupName)
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
		restate.Set(ctx, drivers.StateKey, state)
		return DBSubnetGroupOutputs{}, err
	}

	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Import discovers an existing DB Subnet Group and adopts it into Praxis state.
// Synthesizes a spec from the observed state. Defaults to Observed mode.
func (d *DBSubnetGroupDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (DBSubnetGroupOutputs, error) {
	ctx.Log().Info("importing db subnet group", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return DBSubnetGroupOutputs{}, restate.TerminalError(err, 400)
	}
	mode := defaultImportMode(ref.Mode)
	state, err := restate.Get[DBSubnetGroupState](ctx, drivers.StateKey)
	if err != nil {
		return DBSubnetGroupOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeDBSubnetGroup(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: db subnet group %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return DBSubnetGroupOutputs{}, err
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Mode = mode
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Delete removes the DB Subnet Group. Blocks deletion in Observed mode.
// Deletion is immediate (no wait needed); NotFound is treated as success.
func (d *DBSubnetGroupDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting db subnet group", "key", restate.Key(ctx))
	state, err := restate.Get[DBSubnetGroupState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete db subnet group %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.GroupName), 409)
	}
	groupName := state.Outputs.GroupName
	if groupName == "" {
		groupName = state.Desired.GroupName
	}
	if groupName == "" {
		restate.Set(ctx, drivers.StateKey, DBSubnetGroupState{Status: types.StatusDeleted})
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
		runErr := api.DeleteDBSubnetGroup(rc, groupName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsInvalidState(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 409)
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
	restate.Set(ctx, drivers.StateKey, DBSubnetGroupState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is invoked on a timer to detect and correct drift.
// In Managed mode, corrects description/subnet/tag drift. In Observed mode, only reports.
func (d *DBSubnetGroupDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[DBSubnetGroupState](ctx, drivers.StateKey)
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
	groupName := state.Outputs.GroupName
	if groupName == "" {
		groupName = state.Desired.GroupName
	}
	if groupName == "" {
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
		obs, runErr := api.DescribeDBSubnetGroup(rc, groupName)
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
			state.Error = fmt.Sprintf("db subnet group %s was deleted externally", groupName)
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
	drift := HasDrift(state.Desired, observed)
	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}
	if drift && state.Mode == types.ModeManaged {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		if correctionErr := d.correctDrift(ctx, api, state.Desired, observed); correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		state.Error = ""
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}
	if drift && state.Mode == types.ModeObserved {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{Drift: drift, Correcting: false}, nil
}

// GetStatus is a SHARED handler returning the group's status without exclusive access.
func (d *DBSubnetGroupDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[DBSubnetGroupState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a SHARED handler returning the group's outputs without exclusive access.
func (d *DBSubnetGroupDriver) GetOutputs(ctx restate.ObjectSharedContext) (DBSubnetGroupOutputs, error) {
	state, err := restate.Get[DBSubnetGroupState](ctx, drivers.StateKey)
	if err != nil {
		return DBSubnetGroupOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *DBSubnetGroupDriver) GetInputs(ctx restate.ObjectSharedContext) (DBSubnetGroupSpec, error) {
	state, err := restate.Get[DBSubnetGroupState](ctx, drivers.StateKey)
	if err != nil {
		return DBSubnetGroupSpec{}, err
	}
	return state.Desired, nil
}

// correctDrift applies ModifyDBSubnetGroup and/or UpdateTags to converge.
func (d *DBSubnetGroupDriver) correctDrift(ctx restate.ObjectContext, api DBSubnetGroupAPI, desired DBSubnetGroupSpec, observed ObservedState) error {
	if desired.Description != observed.Description || !stringSliceEqual(desired.SubnetIds, observed.SubnetIds) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.ModifyDBSubnetGroup(rc, desired)
			if runErr != nil {
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("modify db subnet group: %w", err)
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) && observed.ARN != "" {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.ARN, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update db subnet group tags: %w", err)
		}
	}
	return nil
}

// scheduleReconcile enqueues the next reconciliation via a Restate delayed send.
// Uses the ReconcileScheduled flag to prevent timer fan-out.
func (d *DBSubnetGroupDriver) scheduleReconcile(ctx restate.ObjectContext, state *DBSubnetGroupState) {
	if state == nil || state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileIntervalForKind(ServiceName)))
}

// apiForAccount resolves AWS credentials and creates an API client for the given account.
func (d *DBSubnetGroupDriver) apiForAccount(ctx restate.ObjectContext, account string) (DBSubnetGroupAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("db subnet group driver is not configured")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve RDS account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

// validateSpec checks required fields. SubnetIds must have at least 2 entries.
func validateSpec(spec DBSubnetGroupSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.GroupName) == "" {
		return fmt.Errorf("groupName is required")
	}
	if strings.TrimSpace(spec.Description) == "" {
		return fmt.Errorf("description is required")
	}
	if len(spec.SubnetIds) < 2 {
		return fmt.Errorf("subnetIds must contain at least 2 subnets")
	}
	return nil
}

// specFromObserved synthesises a spec from observed state for import.
func specFromObserved(observed ObservedState) DBSubnetGroupSpec {
	return DBSubnetGroupSpec{
		GroupName:   observed.GroupName,
		Description: observed.Description,
		SubnetIds:   normalizeStrings(observed.SubnetIds),
		Tags:        drivers.FilterPraxisTags(observed.Tags),
	}
}

// outputsFromObserved maps observed state to the output struct.
func outputsFromObserved(observed ObservedState) DBSubnetGroupOutputs {
	return DBSubnetGroupOutputs{
		GroupName:         observed.GroupName,
		ARN:               observed.ARN,
		VpcId:             observed.VpcId,
		SubnetIds:         normalizeStrings(observed.SubnetIds),
		AvailabilityZones: normalizeStrings(observed.AvailabilityZones),
		Status:            observed.Status,
	}
}

// defaultImportMode returns ModeObserved if no explicit mode is given.
func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

// ClearState clears all Virtual Object state for this resource.
// Used by the Orphan deletion policy to release a resource from management.
func (d *DBSubnetGroupDriver) ClearState(ctx restate.ObjectContext) error {
	drivers.ClearAllState(ctx)
	return nil
}
