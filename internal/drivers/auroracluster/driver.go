package auroracluster

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

// AuroraClusterDriver is a Restate Virtual Object that manages the lifecycle of
// AWS Aurora clusters. Restate guarantees single-writer access per cluster key,
// eliminating concurrent-mutation races.
type AuroraClusterDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) AuroraClusterAPI
}

// NewAuroraClusterDriver creates a driver backed by real AWS RDS clients.
func NewAuroraClusterDriver(auth authservice.AuthClient) *AuroraClusterDriver {
	return NewAuroraClusterDriverWithFactory(auth, func(cfg aws.Config) AuroraClusterAPI {
		return NewAuroraClusterAPI(awsclient.NewRDSClient(cfg))
	})
}

// NewAuroraClusterDriverWithFactory creates a driver with a custom API factory (for tests).
func NewAuroraClusterDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) AuroraClusterAPI) *AuroraClusterDriver {
	if factory == nil {
		factory = func(cfg aws.Config) AuroraClusterAPI { return NewAuroraClusterAPI(awsclient.NewRDSClient(cfg)) }
	}
	return &AuroraClusterDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate service name for registration.
func (d *AuroraClusterDriver) ServiceName() string {
	return ServiceName
}

// Provision creates or converges an Aurora cluster to match the desired spec.
// Idempotent: if the cluster exists, validates immutable fields and applies drift corrections.
// Detects password rotation when MasterUserPassword changes between specs.
// Waits for the cluster to reach "available" before returning.
func (d *AuroraClusterDriver) Provision(ctx restate.ObjectContext, spec AuroraClusterSpec) (AuroraClusterOutputs, error) {
	api, _, err := d.apiForAccount(ctx, spec.Account)
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

// Import discovers an existing Aurora cluster and adopts it into Praxis state.
// Synthesizes a spec from the observed state. Defaults to Observed mode.
func (d *AuroraClusterDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (AuroraClusterOutputs, error) {
	api, region, err := d.apiForAccount(ctx, ref.Account)
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

// Delete removes the Aurora cluster. Blocks deletion in Observed mode.
// Auto-disables deletion protection if enabled, then deletes with skipFinalSnapshot=true.
// Waits for the cluster to be fully deleted before clearing state.
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

// Reconcile is invoked on a timer to detect and correct drift.
// Validates immutable fields before attempting correction in Managed mode.
// Reports drift events for both Managed and Observed modes.
func (d *AuroraClusterDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[AuroraClusterState](ctx, drivers.StateKey)
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
	if drift && state.Mode == types.ModeManaged && state.Status != types.StatusError {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
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

// GetStatus is a SHARED handler returning the cluster's status without exclusive access.
func (d *AuroraClusterDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[AuroraClusterState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a SHARED handler returning the cluster's outputs without exclusive access.
func (d *AuroraClusterDriver) GetOutputs(ctx restate.ObjectSharedContext) (AuroraClusterOutputs, error) {
	state, err := restate.Get[AuroraClusterState](ctx, drivers.StateKey)
	if err != nil {
		return AuroraClusterOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *AuroraClusterDriver) GetInputs(ctx restate.ObjectSharedContext) (AuroraClusterSpec, error) {
	state, err := restate.Get[AuroraClusterState](ctx, drivers.StateKey)
	if err != nil {
		return AuroraClusterSpec{}, err
	}
	return state.Desired, nil
}

// correctDrift applies ModifyDBCluster and/or UpdateTags to converge the cluster.
// Detects password rotation when MasterUserPassword differs from previousDesired.
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
	if !drivers.TagsMatch(desired.Tags, observed.Tags) && observed.ARN != "" {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.ARN, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update Aurora cluster tags: %w", err)
		}
	}
	return nil
}

// scheduleReconcile enqueues the next reconciliation via a Restate delayed send.
// Uses the ReconcileScheduled flag to prevent timer fan-out.
func (d *AuroraClusterDriver) scheduleReconcile(ctx restate.ObjectContext, state *AuroraClusterState) {
	if state == nil || state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

// apiForAccount resolves AWS credentials and creates an API client for the given account.
func (d *AuroraClusterDriver) apiForAccount(ctx restate.ObjectContext, account string) (AuroraClusterAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("aurora cluster driver is not configured")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve RDS account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

// validateSpec checks that all required fields are present before creating a cluster.
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

// validateExisting checks immutable field constraints against a live cluster.
// Returns an error if clusterIdentifier, engine, masterUsername, or databaseName changed.
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

// specFromObserved synthesises a spec from observed state for import.
func specFromObserved(observed ObservedState) AuroraClusterSpec {
	return applyDefaults(AuroraClusterSpec{ClusterIdentifier: observed.ClusterIdentifier, Engine: observed.Engine, EngineVersion: observed.EngineVersion, MasterUsername: observed.MasterUsername, DatabaseName: observed.DatabaseName, Port: observed.Port, DBSubnetGroupName: observed.DBSubnetGroupName, DBClusterParameterGroupName: observed.DBClusterParameterGroupName, VpcSecurityGroupIds: observed.VpcSecurityGroupIds, StorageEncrypted: observed.StorageEncrypted, KMSKeyId: observed.KMSKeyId, BackupRetentionPeriod: observed.BackupRetentionPeriod, PreferredBackupWindow: observed.PreferredBackupWindow, PreferredMaintenanceWindow: observed.PreferredMaintenanceWindow, DeletionProtection: observed.DeletionProtection, EnabledCloudwatchLogsExports: observed.EnabledCloudwatchLogsExports, Tags: drivers.FilterPraxisTags(observed.Tags)})
}

// outputsFromObserved maps observed state to the output struct.
func outputsFromObserved(observed ObservedState) AuroraClusterOutputs {
	return AuroraClusterOutputs{ClusterIdentifier: observed.ClusterIdentifier, ClusterResourceId: observed.ClusterResourceId, ARN: observed.ARN, Endpoint: observed.Endpoint, ReaderEndpoint: observed.ReaderEndpoint, Port: observed.Port, Engine: observed.Engine, EngineVersion: observed.EngineVersion, Status: observed.Status}
}

// defaultImportMode returns ModeObserved if no explicit mode is given.
func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}
