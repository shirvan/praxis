package dashboard

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

type DashboardDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) DashboardAPI
}

func NewDashboardDriver(auth authservice.AuthClient) *DashboardDriver {
	return NewDashboardDriverWithFactory(auth, func(cfg aws.Config) DashboardAPI {
		return NewDashboardAPI(awsclient.NewCloudWatchClient(cfg))
	})
}

func NewDashboardDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) DashboardAPI) *DashboardDriver {
	if factory == nil {
		factory = func(cfg aws.Config) DashboardAPI {
			return NewDashboardAPI(awsclient.NewCloudWatchClient(cfg))
		}
	}
	return &DashboardDriver{auth: auth, apiFactory: factory}
}

func (d *DashboardDriver) ServiceName() string {
	return ServiceName
}

func (d *DashboardDriver) Provision(ctx restate.ObjectContext, spec DashboardSpec) (DashboardOutputs, error) {
	ctx.Log().Info("provisioning CloudWatch dashboard", "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return DashboardOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	spec.Region = region
	if err := validateSpec(spec); err != nil {
		return DashboardOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[DashboardState](ctx, drivers.StateKey)
	if err != nil {
		return DashboardOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++
	restate.Set(ctx, drivers.StateKey, state)
	validationMessages, err := restate.Run(ctx, func(rc restate.RunContext) ([]ValidationMessage, error) {
		messages, runErr := api.PutDashboard(rc, spec)
		if runErr != nil {
			if IsDashboardInvalidInput(runErr) || IsInvalidParam(runErr) {
				return nil, restate.TerminalError(runErr, 400)
			}
			return nil, runErr
		}
		return messages, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return DashboardOutputs{}, err
	}
	for _, message := range validationMessages {
		ctx.Log().Info("dashboard validation warning", "dashboardName", spec.DashboardName, "dataPath", message.DataPath, "message", message.Message)
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (struct {
		Observed ObservedState
		Found    bool
	}, error) {
		obs, ok, runErr := api.GetDashboard(rc, spec.DashboardName)
		if runErr != nil {
			return struct {
				Observed ObservedState
				Found    bool
			}{}, runErr
		}
		return struct {
			Observed ObservedState
			Found    bool
		}{Observed: obs, Found: ok}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return DashboardOutputs{}, err
	}
	if !observed.Found {
		err := fmt.Errorf("dashboard %s disappeared during provisioning", spec.DashboardName)
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return DashboardOutputs{}, err
	}
	state.Observed = observed.Observed
	state.Outputs = outputsFromObserved(observed.Observed)
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

func (d *DashboardDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (DashboardOutputs, error) {
	ctx.Log().Info("importing CloudWatch dashboard", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return DashboardOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[DashboardState](ctx, drivers.StateKey)
	if err != nil {
		return DashboardOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (struct {
		Observed ObservedState
		Found    bool
	}, error) {
		obs, ok, runErr := api.GetDashboard(rc, ref.ResourceID)
		if runErr != nil {
			return struct {
				Observed ObservedState
				Found    bool
			}{}, runErr
		}
		return struct {
			Observed ObservedState
			Found    bool
		}{Observed: obs, Found: ok}, nil
	})
	if err != nil {
		return DashboardOutputs{}, err
	}
	if !observed.Found {
		return DashboardOutputs{}, restate.TerminalError(fmt.Errorf("import failed: dashboard %s does not exist", ref.ResourceID), 404)
	}
	spec := specFromObserved(observed.Observed)
	spec.Account = ref.Account
	spec.Region = region
	state.Desired = spec
	state.Observed = observed.Observed
	state.Outputs = outputsFromObserved(observed.Observed)
	state.Status = types.StatusReady
	state.Mode = defaultImportMode(ref.Mode)
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

func (d *DashboardDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting CloudWatch dashboard", "key", restate.Key(ctx))
	state, err := restate.Get[DashboardState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete dashboard %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.DashboardName), 409)
	}
	if state.Outputs.DashboardName == "" {
		restate.Set(ctx, drivers.StateKey, DashboardState{Status: types.StatusDeleted})
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
		runErr := api.DeleteDashboard(rc, state.Outputs.DashboardName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsDashboardInvalidInput(runErr) || IsInvalidParam(runErr) {
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
	restate.Set(ctx, drivers.StateKey, DashboardState{Status: types.StatusDeleted})
	return nil
}

func (d *DashboardDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[DashboardState](ctx, drivers.StateKey)
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
	if state.Outputs.DashboardName == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return types.ReconcileResult{}, err
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (struct {
		Observed ObservedState
		Found    bool
	}, error) {
		obs, ok, runErr := api.GetDashboard(rc, state.Outputs.DashboardName)
		if runErr != nil {
			return struct {
				Observed ObservedState
				Found    bool
			}{}, runErr
		}
		return struct {
			Observed ObservedState
			Found    bool
		}{Observed: obs, Found: ok}, nil
	})
	if err != nil {
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}
	if !observed.Found {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("dashboard %s was deleted externally", state.Outputs.DashboardName)
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventExternalDelete, state.Error)
		return types.ReconcileResult{Error: state.Error}, nil
	}
	state.Observed = observed.Observed
	state.Outputs = outputsFromObserved(observed.Observed)
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed.Observed)
	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift}, nil
	}
	if drift && state.Mode == types.ModeManaged {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		validationMessages, correctionErr := restate.Run(ctx, func(rc restate.RunContext) ([]ValidationMessage, error) {
			messages, runErr := api.PutDashboard(rc, state.Desired)
			if runErr != nil {
				if IsDashboardInvalidInput(runErr) || IsInvalidParam(runErr) {
					return nil, restate.TerminalError(runErr, 400)
				}
				return nil, runErr
			}
			return messages, nil
		})
		if correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		for _, message := range validationMessages {
			ctx.Log().Info("dashboard validation warning", "dashboardName", state.Desired.DashboardName, "dataPath", message.DataPath, "message", message.Message)
		}
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
	return types.ReconcileResult{Drift: drift}, nil
}

func (d *DashboardDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[DashboardState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *DashboardDriver) GetOutputs(ctx restate.ObjectSharedContext) (DashboardOutputs, error) {
	state, err := restate.Get[DashboardState](ctx, drivers.StateKey)
	if err != nil {
		return DashboardOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *DashboardDriver) scheduleReconcile(ctx restate.ObjectContext, state *DashboardState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *DashboardDriver) apiForAccount(ctx restate.ObjectContext, account string) (DashboardAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("DashboardDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve Dashboard account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func outputsFromObserved(observed ObservedState) DashboardOutputs {
	return DashboardOutputs{DashboardArn: observed.DashboardArn, DashboardName: observed.DashboardName}
}

func specFromObserved(observed ObservedState) DashboardSpec {
	return DashboardSpec{DashboardName: observed.DashboardName, DashboardBody: observed.DashboardBody}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

func applyDefaults(spec DashboardSpec) DashboardSpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.DashboardName = strings.TrimSpace(spec.DashboardName)
	spec.DashboardBody = strings.TrimSpace(spec.DashboardBody)
	return spec
}

func validateSpec(spec DashboardSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.DashboardName == "" {
		return fmt.Errorf("dashboardName is required")
	}
	if spec.DashboardBody == "" {
		return fmt.Errorf("dashboardBody is required")
	}
	return nil
}
