package lambdaperm

import (
	"encoding/json"
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

// LambdaPermissionDriver implements the Praxis driver for Lambda resource-based permissions.
type LambdaPermissionDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) PermissionAPI
}

// NewLambdaPermissionDriver creates a production driver with default Lambda client factory.
func NewLambdaPermissionDriver(auth authservice.AuthClient) *LambdaPermissionDriver {
	return NewLambdaPermissionDriverWithFactory(auth, func(cfg aws.Config) PermissionAPI {
		return NewPermissionAPI(awsclient.NewLambdaClient(cfg))
	})
}

// NewLambdaPermissionDriverWithFactory creates a driver with a custom PermissionAPI factory (for testing).
func NewLambdaPermissionDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) PermissionAPI) *LambdaPermissionDriver {
	if factory == nil {
		factory = func(cfg aws.Config) PermissionAPI { return NewPermissionAPI(awsclient.NewLambdaClient(cfg)) }
	}
	return &LambdaPermissionDriver{auth: auth, apiFactory: factory}
}

func (d *LambdaPermissionDriver) ServiceName() string { return ServiceName }

// Provision creates or updates a Lambda permission statement.
// Uses a remove-then-add pattern since individual statements cannot be modified.
// If no spec fields changed, the operation is a no-op.
func (d *LambdaPermissionDriver) Provision(ctx restate.ObjectContext, spec LambdaPermissionSpec) (LambdaPermissionOutputs, error) {
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return LambdaPermissionOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	if spec.Region == "" {
		spec.Region = region
	}
	if err := validateProvisionSpec(spec); err != nil {
		return LambdaPermissionOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[LambdaPermissionState](ctx, drivers.StateKey)
	if err != nil {
		return LambdaPermissionOutputs{}, err
	}
	previousDesired := state.Desired
	previousObserved := state.Observed
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++
	if state.Outputs.StatementId != "" && !specChanged(previousDesired, spec) {
		state.Status = types.StatusReady
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return state.Outputs, nil
	}
	if state.Outputs.StatementId != "" {
		_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.RemovePermission(rc, previousObserved.FunctionName, previousObserved.StatementId)
		})
		if err != nil && !IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return LambdaPermissionOutputs{}, err
		}
	}
	statement, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return api.AddPermission(rc, spec)
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		if IsConflict(err) || IsPreconditionFailed(err) {
			return LambdaPermissionOutputs{}, restate.TerminalError(err, 409)
		}
		return LambdaPermissionOutputs{}, err
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.GetPermission(rc, spec.FunctionName, spec.StatementId)
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return LambdaPermissionOutputs{}, err
	}
	state.Observed = observed
	state.Outputs = LambdaPermissionOutputs{StatementId: spec.StatementId, FunctionName: spec.FunctionName, Statement: statement}
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Import adopts an existing Lambda permission into Praxis management.
// ResourceID format: "functionName~statementId" (split on ~).
func (d *LambdaPermissionDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (LambdaPermissionOutputs, error) {
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return LambdaPermissionOutputs{}, restate.TerminalError(err, 400)
	}
	functionName, statementID, err := splitImportResourceID(ref.ResourceID)
	if err != nil {
		return LambdaPermissionOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[LambdaPermissionState](ctx, drivers.StateKey)
	if err != nil {
		return LambdaPermissionOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.GetPermission(rc, functionName, statementID)
	})
	if err != nil {
		if IsNotFound(err) {
			return LambdaPermissionOutputs{}, restate.TerminalError(fmt.Errorf("import failed: Lambda permission %s does not exist", ref.ResourceID), 404)
		}
		return LambdaPermissionOutputs{}, err
	}
	policy, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return api.GetPolicy(rc, functionName)
	})
	if err != nil {
		return LambdaPermissionOutputs{}, err
	}
	statement, err := permissionStatementFromPolicy(policy, statementID)
	if err != nil {
		return LambdaPermissionOutputs{}, restate.TerminalError(err, 404)
	}
	rawStatement, _ := jsonMarshal(statement)
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region
	state.Desired = spec
	state.Observed = observed
	state.Outputs = LambdaPermissionOutputs{StatementId: observed.StatementId, FunctionName: observed.FunctionName, Statement: rawStatement}
	state.Status = types.StatusReady
	state.Mode = defaultImportMode(ref.Mode)
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Delete removes the permission statement. Observed-mode resources cannot be deleted (409).
func (d *LambdaPermissionDriver) Delete(ctx restate.ObjectContext) error {
	state, err := restate.Get[LambdaPermissionState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete Lambda permission %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.StatementId), 409)
	}
	if state.Outputs.StatementId == "" {
		restate.Set(ctx, drivers.StateKey, LambdaPermissionState{Status: types.StatusDeleted})
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
		deleteErr := api.RemovePermission(rc, state.Observed.FunctionName, state.Observed.StatementId)
		if deleteErr != nil && !IsNotFound(deleteErr) {
			return restate.Void{}, deleteErr
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}
	restate.Set(ctx, drivers.StateKey, LambdaPermissionState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-detection loop.
// Detects external deletion and field drift. No auto-correction is performed
// since permissions are replace-only (would require remove + re-add).
func (d *LambdaPermissionDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[LambdaPermissionState](ctx, drivers.StateKey)
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
	if state.Outputs.StatementId == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) { return time.Now().UTC().Format(time.RFC3339), nil })
	if err != nil {
		return types.ReconcileResult{}, err
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.GetPermission(rc, state.Observed.FunctionName, state.Observed.StatementId)
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("Lambda permission %s was deleted externally", state.Outputs.StatementId)
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
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed)
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	if drift {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
	}
	return types.ReconcileResult{Drift: drift}, nil
}

// GetStatus returns the current lifecycle status (shared/concurrent handler).
func (d *LambdaPermissionDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[LambdaPermissionState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs returns the provisioned outputs (shared/concurrent handler).
func (d *LambdaPermissionDriver) GetOutputs(ctx restate.ObjectSharedContext) (LambdaPermissionOutputs, error) {
	state, err := restate.Get[LambdaPermissionState](ctx, drivers.StateKey)
	if err != nil {
		return LambdaPermissionOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *LambdaPermissionDriver) GetInputs(ctx restate.ObjectSharedContext) (LambdaPermissionSpec, error) {
	state, err := restate.Get[LambdaPermissionState](ctx, drivers.StateKey)
	if err != nil {
		return LambdaPermissionSpec{}, err
	}
	return state.Desired, nil
}

// scheduleReconcile enqueues a delayed Reconcile message with dedup guard.
func (d *LambdaPermissionDriver) scheduleReconcile(ctx restate.ObjectContext, state *LambdaPermissionState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

// apiForAccount resolves AWS credentials and creates a PermissionAPI for the given Praxis account.
func (d *LambdaPermissionDriver) apiForAccount(ctx restate.ObjectContext, account string) (PermissionAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("LambdaPermissionDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve Lambda permission account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

// applyDefaults sets Action to "lambda:InvokeFunction" if empty.
func applyDefaults(spec LambdaPermissionSpec) LambdaPermissionSpec {
	if strings.TrimSpace(spec.Action) == "" {
		spec.Action = "lambda:InvokeFunction"
	}
	return spec
}

// validateProvisionSpec checks that region, functionName, statementId, and principal are set.
func validateProvisionSpec(spec LambdaPermissionSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.FunctionName) == "" {
		return fmt.Errorf("functionName is required")
	}
	if strings.TrimSpace(spec.StatementId) == "" {
		return fmt.Errorf("statementId is required")
	}
	if strings.TrimSpace(spec.Principal) == "" {
		return fmt.Errorf("principal is required")
	}
	return nil
}

// specChanged returns true if any spec field differs between old and new.
func specChanged(oldSpec, newSpec LambdaPermissionSpec) bool {
	return oldSpec.FunctionName != newSpec.FunctionName ||
		oldSpec.StatementId != newSpec.StatementId ||
		oldSpec.Action != newSpec.Action ||
		oldSpec.Principal != newSpec.Principal ||
		oldSpec.SourceArn != newSpec.SourceArn ||
		oldSpec.SourceAccount != newSpec.SourceAccount ||
		oldSpec.EventSourceToken != newSpec.EventSourceToken ||
		oldSpec.Qualifier != newSpec.Qualifier
}

// specFromObserved reconstructs a LambdaPermissionSpec from observed state for Import.
func specFromObserved(observed ObservedState) LambdaPermissionSpec {
	return applyDefaults(LambdaPermissionSpec{FunctionName: observed.FunctionName, StatementId: observed.StatementId, Action: observed.Action, Principal: observed.Principal, SourceArn: observed.SourceArn, SourceAccount: observed.SourceAccount, EventSourceToken: observed.EventSourceToken})
}

// defaultImportMode returns Observed if no mode was explicitly specified.
func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

// splitImportResourceID parses "functionName~statementId" format.
func splitImportResourceID(resourceID string) (string, string, error) {
	parts := strings.SplitN(resourceID, "~", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("import resource ID must be functionName~statementId")
	}
	return parts[0], parts[1], nil
}

func jsonMarshal(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
