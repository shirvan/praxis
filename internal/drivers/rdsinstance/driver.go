package rdsinstance

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

type RDSInstanceDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) RDSInstanceAPI
}

func NewRDSInstanceDriver(auth authservice.AuthClient) *RDSInstanceDriver {
	return NewRDSInstanceDriverWithFactory(auth, func(cfg aws.Config) RDSInstanceAPI {
		return NewRDSInstanceAPI(awsclient.NewRDSClient(cfg))
	})
}

func NewRDSInstanceDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) RDSInstanceAPI) *RDSInstanceDriver {
	if factory == nil {
		factory = func(cfg aws.Config) RDSInstanceAPI { return NewRDSInstanceAPI(awsclient.NewRDSClient(cfg)) }
	}
	return &RDSInstanceDriver{auth: auth, apiFactory: factory}
}

func (d *RDSInstanceDriver) ServiceName() string {
	return ServiceName
}

func (d *RDSInstanceDriver) Provision(ctx restate.ObjectContext, spec RDSInstanceSpec) (RDSInstanceOutputs, error) {
	api, _, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return RDSInstanceOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	if err := validateSpec(spec); err != nil {
		return RDSInstanceOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[RDSInstanceState](ctx, drivers.StateKey)
	if err != nil {
		return RDSInstanceOutputs{}, err
	}
	state.Generation++
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	previousDesired := state.Desired
	state.Desired = spec
	observed, describeErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeDBInstance(rc, spec.DBIdentifier)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, nil
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if describeErr != nil {
		state.Status = types.StatusError
		state.Error = describeErr.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return RDSInstanceOutputs{}, describeErr
	}
	if observed.DBIdentifier == "" {
		_, err = restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			arn, runErr := api.CreateDBInstance(rc, spec)
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
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return RDSInstanceOutputs{}, err
		}
	} else {
		if err := validateExisting(spec, observed); err != nil {
			return RDSInstanceOutputs{}, restate.TerminalError(err, 409)
		}
		if correctionErr := d.correctDrift(ctx, api, spec, observed, previousDesired); correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			state.Observed = observed
			state.Outputs = outputsFromObserved(observed)
			restate.Set(ctx, drivers.StateKey, state)
			return RDSInstanceOutputs{}, correctionErr
		}
	}
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.WaitUntilAvailable(rc, spec.DBIdentifier)
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return RDSInstanceOutputs{}, err
	}
	observed, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.DescribeDBInstance(rc, spec.DBIdentifier)
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return RDSInstanceOutputs{}, err
	}
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

func (d *RDSInstanceDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (RDSInstanceOutputs, error) {
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return RDSInstanceOutputs{}, restate.TerminalError(err, 400)
	}
	mode := defaultImportMode(ref.Mode)
	state, err := restate.Get[RDSInstanceState](ctx, drivers.StateKey)
	if err != nil {
		return RDSInstanceOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeDBInstance(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: RDS instance %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return RDSInstanceOutputs{}, err
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

func (d *RDSInstanceDriver) Delete(ctx restate.ObjectContext) error {
	state, err := restate.Get[RDSInstanceState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete RDS instance %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.DBIdentifier), 409)
	}
	identifier := state.Outputs.DBIdentifier
	if identifier == "" {
		identifier = state.Desired.DBIdentifier
	}
	if identifier == "" {
		restate.Set(ctx, drivers.StateKey, RDSInstanceState{Status: types.StatusDeleted})
		return nil
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}
	if state.Observed.DeletionProtection {
		spec := state.Desired
		spec.DeletionProtection = false
		if err := d.correctDrift(ctx, api, spec, state.Observed, state.Desired); err != nil {
			return err
		}
		_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.WaitUntilAvailable(rc, identifier)
		})
		if err != nil {
			return err
		}
	}
	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteDBInstance(rc, identifier, true)
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
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		waitErr := api.WaitUntilDeleted(rc, identifier)
		if waitErr != nil && !IsNotFound(waitErr) {
			return restate.Void{}, waitErr
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}
	restate.Set(ctx, drivers.StateKey, RDSInstanceState{Status: types.StatusDeleted})
	return nil
}

func (d *RDSInstanceDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[RDSInstanceState](ctx, drivers.StateKey)
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
	identifier := state.Outputs.DBIdentifier
	if identifier == "" {
		identifier = state.Desired.DBIdentifier
	}
	if identifier == "" {
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
		return api.DescribeDBInstance(rc, identifier)
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("RDS instance %s was deleted externally", identifier)
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
	state.Outputs = outputsFromObserved(observed)
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed)
	if drift && state.Mode == types.ModeManaged && state.Status != types.StatusError {
		if err := validateExisting(state.Desired, observed); err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: false, Error: err.Error()}, nil
		}
		if correctionErr := d.correctDrift(ctx, api, state.Desired, observed, state.Desired); correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		state.Error = ""
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{Drift: drift, Correcting: false}, nil
}

func (d *RDSInstanceDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[RDSInstanceState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *RDSInstanceDriver) GetOutputs(ctx restate.ObjectSharedContext) (RDSInstanceOutputs, error) {
	state, err := restate.Get[RDSInstanceState](ctx, drivers.StateKey)
	if err != nil {
		return RDSInstanceOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *RDSInstanceDriver) correctDrift(ctx restate.ObjectContext, api RDSInstanceAPI, desired RDSInstanceSpec, observed ObservedState, previousDesired RDSInstanceSpec) error {
	if desired.AllocatedStorage > 0 && observed.AllocatedStorage > 0 && desired.AllocatedStorage < observed.AllocatedStorage {
		return restate.TerminalError(fmt.Errorf("allocatedStorage cannot be reduced from %d to %d", observed.AllocatedStorage, desired.AllocatedStorage), 400)
	}
	needsModify := HasDrift(desired, observed) || (desired.MasterUserPassword != "" && desired.MasterUserPassword != previousDesired.MasterUserPassword)
	if needsModify {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.ModifyDBInstance(rc, desired, true)
			if runErr != nil {
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				if IsInvalidState(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 409)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("modify RDS instance: %w", err)
		}
	}
	if !tagsMatch(desired.Tags, observed.Tags) && observed.ARN != "" {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.ARN, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update RDS instance tags: %w", err)
		}
	}
	return nil
}

func (d *RDSInstanceDriver) scheduleReconcile(ctx restate.ObjectContext, state *RDSInstanceState) {
	if state == nil || state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *RDSInstanceDriver) apiForAccount(ctx restate.ObjectContext, account string) (RDSInstanceAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("RDS instance driver is not configured")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve RDS account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func validateSpec(spec RDSInstanceSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.DBIdentifier) == "" {
		return fmt.Errorf("dbIdentifier is required")
	}
	if strings.TrimSpace(spec.Engine) == "" {
		return fmt.Errorf("engine is required")
	}
	if strings.TrimSpace(spec.EngineVersion) == "" {
		return fmt.Errorf("engineVersion is required")
	}
	if strings.TrimSpace(spec.InstanceClass) == "" {
		return fmt.Errorf("instanceClass is required")
	}
	if spec.DBClusterIdentifier == "" {
		if spec.AllocatedStorage <= 0 {
			return fmt.Errorf("allocatedStorage is required for non-Aurora RDS instances")
		}
		if strings.TrimSpace(spec.MasterUsername) == "" {
			return fmt.Errorf("masterUsername is required for non-Aurora RDS instances")
		}
		if strings.TrimSpace(spec.MasterUserPassword) == "" {
			return fmt.Errorf("masterUserPassword is required for non-Aurora RDS instances")
		}
	}
	if spec.MonitoringInterval > 0 && strings.TrimSpace(spec.MonitoringRoleArn) == "" {
		return fmt.Errorf("monitoringRoleArn is required when monitoringInterval > 0")
	}
	return nil
}

func validateExisting(spec RDSInstanceSpec, observed ObservedState) error {
	if observed.DBIdentifier != "" && spec.DBIdentifier != observed.DBIdentifier {
		return fmt.Errorf("dbIdentifier is immutable: desired %q, observed %q", spec.DBIdentifier, observed.DBIdentifier)
	}
	if observed.Engine != "" && spec.Engine != observed.Engine {
		return fmt.Errorf("engine is immutable: desired %q, observed %q", spec.Engine, observed.Engine)
	}
	if observed.MasterUsername != "" && spec.MasterUsername != "" && spec.MasterUsername != observed.MasterUsername {
		return fmt.Errorf("masterUsername is immutable: desired %q, observed %q", spec.MasterUsername, observed.MasterUsername)
	}
	if observed.DBClusterIdentifier != "" && spec.DBClusterIdentifier != observed.DBClusterIdentifier {
		return fmt.Errorf("dbClusterIdentifier is immutable: desired %q, observed %q", spec.DBClusterIdentifier, observed.DBClusterIdentifier)
	}
	return nil
}

func specFromObserved(observed ObservedState) RDSInstanceSpec {
	return applyDefaults(RDSInstanceSpec{
		DBIdentifier:               observed.DBIdentifier,
		Engine:                     observed.Engine,
		EngineVersion:              observed.EngineVersion,
		InstanceClass:              observed.InstanceClass,
		AllocatedStorage:           observed.AllocatedStorage,
		StorageType:                observed.StorageType,
		IOPS:                       observed.IOPS,
		StorageThroughput:          observed.StorageThroughput,
		StorageEncrypted:           observed.StorageEncrypted,
		KMSKeyId:                   observed.KMSKeyId,
		MasterUsername:             observed.MasterUsername,
		DBSubnetGroupName:          observed.DBSubnetGroupName,
		ParameterGroupName:         observed.ParameterGroupName,
		VpcSecurityGroupIds:        observed.VpcSecurityGroupIds,
		DBClusterIdentifier:        observed.DBClusterIdentifier,
		MultiAZ:                    observed.MultiAZ,
		PubliclyAccessible:         observed.PubliclyAccessible,
		BackupRetentionPeriod:      observed.BackupRetentionPeriod,
		PreferredBackupWindow:      observed.PreferredBackupWindow,
		PreferredMaintenanceWindow: observed.PreferredMaintenanceWindow,
		DeletionProtection:         observed.DeletionProtection,
		AutoMinorVersionUpgrade:    observed.AutoMinorVersionUpgrade,
		MonitoringInterval:         observed.MonitoringInterval,
		MonitoringRoleArn:          observed.MonitoringRoleArn,
		PerformanceInsightsEnabled: observed.PerformanceInsightsEnabled,
		Tags:                       filterPraxisTags(observed.Tags),
	})
}

func outputsFromObserved(observed ObservedState) RDSInstanceOutputs {
	return RDSInstanceOutputs{DBIdentifier: observed.DBIdentifier, DbiResourceId: observed.DbiResourceId, ARN: observed.ARN, Endpoint: observed.Endpoint, Port: observed.Port, Engine: observed.Engine, EngineVersion: observed.EngineVersion, Status: observed.Status}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}
