package lambdaperm

import (
	"encoding/json"
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

type LambdaPermissionDriver struct {
	auth       *auth.Registry
	apiFactory func(aws.Config) PermissionAPI
}

func NewLambdaPermissionDriver(accounts *auth.Registry) *LambdaPermissionDriver {
	return NewLambdaPermissionDriverWithFactory(accounts, func(cfg aws.Config) PermissionAPI {
		return NewPermissionAPI(awsclient.NewLambdaClient(cfg))
	})
}

func NewLambdaPermissionDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) PermissionAPI) *LambdaPermissionDriver {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	if factory == nil {
		factory = func(cfg aws.Config) PermissionAPI { return NewPermissionAPI(awsclient.NewLambdaClient(cfg)) }
	}
	return &LambdaPermissionDriver{auth: accounts, apiFactory: factory}
}

func (d *LambdaPermissionDriver) ServiceName() string { return ServiceName }

func (d *LambdaPermissionDriver) Provision(ctx restate.ObjectContext, spec LambdaPermissionSpec) (LambdaPermissionOutputs, error) {
	api, region, err := d.apiForAccount(spec.Account)
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

func (d *LambdaPermissionDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (LambdaPermissionOutputs, error) {
	api, region, err := d.apiForAccount(ref.Account)
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
	api, _, err := d.apiForAccount(state.Desired.Account)
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

func (d *LambdaPermissionDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[LambdaPermissionState](ctx, drivers.StateKey)
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
	return types.ReconcileResult{Drift: drift}, nil
}

func (d *LambdaPermissionDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[LambdaPermissionState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *LambdaPermissionDriver) GetOutputs(ctx restate.ObjectSharedContext) (LambdaPermissionOutputs, error) {
	state, err := restate.Get[LambdaPermissionState](ctx, drivers.StateKey)
	if err != nil {
		return LambdaPermissionOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *LambdaPermissionDriver) scheduleReconcile(ctx restate.ObjectContext, state *LambdaPermissionState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *LambdaPermissionDriver) apiForAccount(account string) (PermissionAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("LambdaPermissionDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.Resolve(account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve Lambda permission account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func applyDefaults(spec LambdaPermissionSpec) LambdaPermissionSpec {
	if strings.TrimSpace(spec.Action) == "" {
		spec.Action = "lambda:InvokeFunction"
	}
	return spec
}

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

func specFromObserved(observed ObservedState) LambdaPermissionSpec {
	return applyDefaults(LambdaPermissionSpec{FunctionName: observed.FunctionName, StatementId: observed.StatementId, Action: observed.Action, Principal: observed.Principal, SourceArn: observed.SourceArn, SourceAccount: observed.SourceAccount, EventSourceToken: observed.EventSourceToken})
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

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
