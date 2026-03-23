package auroracluster

import (
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type AuroraClusterDriver struct {
	auth       *auth.Registry
	apiFactory func(aws.Config) AuroraClusterAPI
}

func NewAuroraClusterDriver(accounts *auth.Registry) *AuroraClusterDriver {
	return NewAuroraClusterDriverWithFactory(accounts, func(cfg aws.Config) AuroraClusterAPI {
		return NewAuroraClusterAPI(awsclient.NewRDSClient(cfg))
	})
}

func NewAuroraClusterDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) AuroraClusterAPI) *AuroraClusterDriver {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	if factory == nil {
		factory = func(cfg aws.Config) AuroraClusterAPI { return NewAuroraClusterAPI(awsclient.NewRDSClient(cfg)) }
	}
	return &AuroraClusterDriver{auth: accounts, apiFactory: factory}
}

func (d *AuroraClusterDriver) ServiceName() string {
	return ServiceName
}

func (d *AuroraClusterDriver) Provision(ctx restate.ObjectContext, spec AuroraClusterSpec) (AuroraClusterOutputs, error) {
	api, _, err := d.apiForAccount(spec.Account)
	if err != nil {
		return AuroraClusterOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	if err := validateSpec(spec); err != nil {
		return AuroraClusterOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[AuroraClusterState](ctx, drivers.StateKey)
	if err != nil {
		return AuroraClusterOutputs{}, err
	}
	state.Generation++
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	previousDesired := state.Desired
	state.Desired = spec
	observed, describeErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeDBCluster(rc, spec.ClusterIdentifier)
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
		return AuroraClusterOutputs{}, describeErr
	}
	if observed.ClusterIdentifier == "" {
		_, err = restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			arn, runErr := api.CreateDBCluster(rc, spec)
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
			return AuroraClusterOutputs{}, err
		}
	} else {
		if err := validateExisting(spec, observed); err != nil {
			return AuroraClusterOutputs{}, restate.TerminalError(err, 409)
		}
		if correctionErr := d.correctDrift(ctx, api, spec, observed, previousDesired); correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			state.Observed = observed
			state.Outputs = outputsFromObserved(observed)
			restate.Set(ctx, drivers.StateKey, state)
			return AuroraClusterOutputs{}, correctionErr
		}
	}
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.WaitUntilAvailable(rc, spec.ClusterIdentifier)
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return AuroraClusterOutputs{}, err
	}
	observed, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.DescribeDBCluster(rc, spec.ClusterIdentifier)
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return AuroraClusterOutputs{}, err
	}
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

func (d *AuroraClusterDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (AuroraClusterOutputs, error) {
	api, region, err := d.apiForAccount(ref.Account)
	if err != nil {
		return AuroraClusterOutputs{}, restate.TerminalError(err, 400)
	}
	mode := defaultImportMode(ref.Mode)
	state, err := restate.Get[AuroraClusterState](ctx, drivers.StateKey)
	if err != nil {
		return AuroraClusterOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeDBCluster(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: Aurora cluster %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return AuroraClusterOutputs{}, err
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

func (d *AuroraClusterDriver) Delete(ctx restate.ObjectContext) error {
	state, err := restate.Get[AuroraClusterState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete Aurora cluster %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.ClusterIdentifier), 409)
	}
	identifier := state.Outputs.ClusterIdentifier
	if identifier == "" {
		identifier = state.Desired.ClusterIdentifier
	}
	if identifier == "" {
		restate.Set(ctx, drivers.StateKey, AuroraClusterState{Status: types.StatusDeleted})
		return nil
	}
	api, _, err := d.apiForAccount(state.Desired.Account)
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
		runErr := api.DeleteDBCluster(rc, identifier, true)
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
	restate.Set(ctx, drivers.StateKey, AuroraClusterState{Status: types.StatusDeleted})
	return nil
}

func (d *AuroraClusterDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[AuroraClusterState](ctx, drivers.StateKey)
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
	identifier := state.Outputs.ClusterIdentifier
	if identifier == "" {
		identifier = state.Desired.ClusterIdentifier
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
		return api.DescribeDBCluster(rc, identifier)
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("Aurora cluster %s was deleted externally", identifier)
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

func (d *AuroraClusterDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[AuroraClusterState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *AuroraClusterDriver) GetOutputs(ctx restate.ObjectSharedContext) (AuroraClusterOutputs, error) {
	state, err := restate.Get[AuroraClusterState](ctx, drivers.StateKey)
	if err != nil {
		return AuroraClusterOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *AuroraClusterDriver) correctDrift(ctx restate.ObjectContext, api AuroraClusterAPI, desired AuroraClusterSpec, observed ObservedState, previousDesired AuroraClusterSpec) error {
	needsModify := HasDrift(desired, observed) || (desired.MasterUserPassword != "" && desired.MasterUserPassword != previousDesired.MasterUserPassword)
	if needsModify {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.ModifyDBCluster(rc, desired, true)
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
			return fmt.Errorf("modify Aurora cluster: %w", err)
		}
	}
	if !tagsMatch(desired.Tags, observed.Tags) && observed.ARN != "" {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.ARN, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update Aurora cluster tags: %w", err)
		}
	}
	return nil
}

func (d *AuroraClusterDriver) scheduleReconcile(ctx restate.ObjectContext, state *AuroraClusterState) {
	if state == nil || state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *AuroraClusterDriver) apiForAccount(account string) (AuroraClusterAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("Aurora cluster driver is not configured")
	}
	acct, err := d.auth.Lookup(account)
	if err != nil {
		return nil, "", err
	}
	awsCfg, err := d.auth.Resolve(account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve RDS account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), acct.Region, nil
}

func validateSpec(spec AuroraClusterSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.ClusterIdentifier) == "" {
		return fmt.Errorf("clusterIdentifier is required")
	}
	if strings.TrimSpace(spec.Engine) == "" {
		return fmt.Errorf("engine is required")
	}
	if strings.TrimSpace(spec.EngineVersion) == "" {
		return fmt.Errorf("engineVersion is required")
	}
	if strings.TrimSpace(spec.MasterUsername) == "" {
		return fmt.Errorf("masterUsername is required")
	}
	if strings.TrimSpace(spec.MasterUserPassword) == "" {
		return fmt.Errorf("masterUserPassword is required")
	}
	return nil
}

func validateExisting(spec AuroraClusterSpec, observed ObservedState) error {
	if observed.ClusterIdentifier != "" && spec.ClusterIdentifier != observed.ClusterIdentifier {
		return fmt.Errorf("clusterIdentifier is immutable: desired %q, observed %q", spec.ClusterIdentifier, observed.ClusterIdentifier)
	}
	if observed.Engine != "" && spec.Engine != observed.Engine {
		return fmt.Errorf("engine is immutable: desired %q, observed %q", spec.Engine, observed.Engine)
	}
	if observed.MasterUsername != "" && spec.MasterUsername != observed.MasterUsername {
		return fmt.Errorf("masterUsername is immutable: desired %q, observed %q", spec.MasterUsername, observed.MasterUsername)
	}
	if observed.DatabaseName != "" && spec.DatabaseName != "" && spec.DatabaseName != observed.DatabaseName {
		return fmt.Errorf("databaseName is immutable: desired %q, observed %q", spec.DatabaseName, observed.DatabaseName)
	}
	return nil
}

func specFromObserved(observed ObservedState) AuroraClusterSpec {
	return applyDefaults(AuroraClusterSpec{ClusterIdentifier: observed.ClusterIdentifier, Engine: observed.Engine, EngineVersion: observed.EngineVersion, MasterUsername: observed.MasterUsername, DatabaseName: observed.DatabaseName, Port: observed.Port, DBSubnetGroupName: observed.DBSubnetGroupName, DBClusterParameterGroupName: observed.DBClusterParameterGroupName, VpcSecurityGroupIds: observed.VpcSecurityGroupIds, StorageEncrypted: observed.StorageEncrypted, KMSKeyId: observed.KMSKeyId, BackupRetentionPeriod: observed.BackupRetentionPeriod, PreferredBackupWindow: observed.PreferredBackupWindow, PreferredMaintenanceWindow: observed.PreferredMaintenanceWindow, DeletionProtection: observed.DeletionProtection, EnabledCloudwatchLogsExports: observed.EnabledCloudwatchLogsExports, Tags: filterPraxisTags(observed.Tags)})
}

func outputsFromObserved(observed ObservedState) AuroraClusterOutputs {
	return AuroraClusterOutputs{ClusterIdentifier: observed.ClusterIdentifier, ClusterResourceId: observed.ClusterResourceId, ARN: observed.ARN, Endpoint: observed.Endpoint, ReaderEndpoint: observed.ReaderEndpoint, Port: observed.Port, Engine: observed.Engine, EngineVersion: observed.EngineVersion, Status: observed.Status}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}
