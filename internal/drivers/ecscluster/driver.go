// Package ecscluster – driver.go
//
// This file implements the Restate Virtual Object handler for AWS ECS clusters.
// The driver exposes durable handlers:
//   - Provision: create-or-converge the cluster and persist state
//   - Import:    adopt an existing AWS cluster into Praxis management
//   - Delete:    remove the cluster from AWS (managed mode only)
//   - Reconcile: periodic drift check + auto-correction (managed mode)
//   - GetStatus / GetOutputs / GetInputs: read-only shared handlers
//
// All mutating AWS calls are wrapped in restate.Run for durable execution,
// and reconciliation is self-scheduled via delayed Restate messages.
package ecscluster

import (
	"fmt"
	"sort"
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

// ECSClusterDriver is the Restate Virtual Object handler for AWS ECS clusters.
// It holds an auth client (for cross-account credential resolution) and an API
// factory (swappable for testing).
type ECSClusterDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) ECSClusterAPI
}

// NewECSClusterDriver creates an ECSCluster driver wired to the given auth
// client. It uses the default AWS SDK client factory.
func NewECSClusterDriver(auth authservice.AuthClient) *ECSClusterDriver {
	return NewECSClusterDriverWithFactory(auth, nil)
}

// NewECSClusterDriverWithFactory creates an ECSCluster driver with a custom API
// factory, primarily used in tests to inject mock AWS clients.
func NewECSClusterDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) ECSClusterAPI) *ECSClusterDriver {
	if factory == nil {
		factory = func(cfg aws.Config) ECSClusterAPI {
			return NewECSClusterAPI(awsclient.NewECSClient(cfg))
		}
	}
	return &ECSClusterDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object service name for registration.
func (d *ECSClusterDriver) ServiceName() string {
	return ServiceName
}

// Provision creates or converges an ECS cluster. It validates the spec, checks
// for an existing cluster, and either creates a new one or converges mutable
// fields on the existing one. State is persisted in Restate K/V after each step.
func (d *ECSClusterDriver) Provision(ctx restate.ObjectContext, spec ECSClusterSpec) (ECSClusterOutputs, error) {
	ctx.Log().Info("provisioning ECS cluster", "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return ECSClusterOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	spec.Region = region
	spec.ManagedKey = restate.Key(ctx)
	if err := validateSpec(spec); err != nil {
		return ECSClusterOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[ECSClusterState](ctx, drivers.StateKey)
	if err != nil {
		return ECSClusterOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	observed, found, err := d.observeCluster(ctx, api, spec.Name)
	if err != nil {
		return d.failProvision(ctx, state, err)
	}

	if !found {
		created, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.CreateCluster(rc, spec)
			if runErr != nil {
				if IsInvalidParam(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 400)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if err != nil {
			return d.failProvision(ctx, state, err)
		}
		observed = created
	}

	if err := d.convergeMutableFields(ctx, api, spec, observed); err != nil {
		return d.failProvision(ctx, state, err)
	}

	observed, found, err = d.observeCluster(ctx, api, spec.Name)
	if err != nil {
		return d.failProvision(ctx, state, err)
	}
	if !found {
		return d.failProvision(ctx, state, fmt.Errorf("cluster %s disappeared during provisioning", spec.Name))
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

// Import adopts an existing ECS cluster into Praxis management. It reads the
// current configuration from AWS, synthesizes a spec from the observed state,
// and stores it. Default import mode is Observed (read-only); users can
// re-import with --mode managed to enable writes.
func (d *ECSClusterDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (ECSClusterOutputs, error) {
	ctx.Log().Info("importing ECS cluster", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return ECSClusterOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[ECSClusterState](ctx, drivers.StateKey)
	if err != nil {
		return ECSClusterOutputs{}, err
	}
	state.Generation++
	observed, found, err := d.observeCluster(ctx, api, ref.ResourceID)
	if err != nil {
		return ECSClusterOutputs{}, err
	}
	if !found {
		return ECSClusterOutputs{}, restate.TerminalError(fmt.Errorf("import failed: cluster %s does not exist", ref.ResourceID), 404)
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region
	spec.ManagedKey = restate.Key(ctx)
	outputs := outputsFromObserved(observed)
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Mode = defaultImportMode(ref.Mode)
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Delete removes the ECS cluster from AWS. It is blocked for resources in
// Observed mode. The method handles not-found gracefully (idempotent delete)
// and sets the final state to StatusDeleted.
func (d *ECSClusterDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting ECS cluster", "key", restate.Key(ctx))
	state, err := restate.Get[ECSClusterState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete cluster %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.Name), 409)
	}
	if state.Outputs.Name == "" {
		restate.Set(ctx, drivers.StateKey, ECSClusterState{Status: types.StatusDeleted})
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
		runErr := api.DeleteCluster(rc, state.Outputs.Name)
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
	restate.Set(ctx, drivers.StateKey, ECSClusterState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-check handler. It re-reads the cluster from
// AWS, compares against desired state, and auto-corrects drift when in Managed
// mode. In Observed mode it only reports drift. External deletions are detected
// and flagged as errors. The handler self-schedules via a delayed message.
func (d *ECSClusterDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[ECSClusterState](ctx, drivers.StateKey)
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
	if state.Outputs.Name == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return types.ReconcileResult{}, err
	}
	observed, found, err := d.observeCluster(ctx, api, state.Outputs.Name)
	if err != nil {
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}
	if !found {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("cluster %s was deleted externally", state.Outputs.Name)
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventExternalDelete, state.Error)
		return types.ReconcileResult{Error: state.Error}, nil
	}
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed)
	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift}, nil
	}
	if drift && state.Mode == types.ModeManaged {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		if correctionErr := d.convergeMutableFields(ctx, api, state.Desired, observed); correctionErr != nil {
			state.Status = types.StatusError
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
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{Drift: drift}, nil
}

// GetStatus is a shared (read-only) handler that returns the current lifecycle status.
func (d *ECSClusterDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[ECSClusterState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a shared (read-only) handler that returns the provisioned resource outputs.
func (d *ECSClusterDriver) GetOutputs(ctx restate.ObjectSharedContext) (ECSClusterOutputs, error) {
	state, err := restate.Get[ECSClusterState](ctx, drivers.StateKey)
	if err != nil {
		return ECSClusterOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *ECSClusterDriver) GetInputs(ctx restate.ObjectSharedContext) (ECSClusterSpec, error) {
	state, err := restate.Get[ECSClusterState](ctx, drivers.StateKey)
	if err != nil {
		return ECSClusterSpec{}, err
	}
	return state.Desired, nil
}

// convergeMutableFields brings an existing cluster in line with the desired
// spec: Container Insights settings, capacity providers, and tags. An ECS
// cluster has no immutable spec fields, so every divergence is corrected here.
func (d *ECSClusterDriver) convergeMutableFields(ctx restate.ObjectContext, api ECSClusterAPI, spec ECSClusterSpec, observed ObservedState) error {
	if containerInsightsDrift(spec, observed) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.UpdateCluster(rc, spec.Name, normalizeContainerInsights(spec.ContainerInsights))
			if runErr != nil {
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return err
		}
	}

	if capacityProvidersDrift(spec, observed) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.PutCapacityProviders(rc, spec.Name, spec.CapacityProviders)
			if runErr != nil {
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return err
		}
	}

	toAdd, toRemove := tagDiff(spec.Tags, observed.Tags, spec.ManagedKey)
	if len(toRemove) > 0 {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UntagResource(rc, observed.ARN, toRemove)
		})
		if err != nil {
			return err
		}
	}
	if len(toAdd) > 0 {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.TagResource(rc, observed.ARN, toAdd)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *ECSClusterDriver) failProvision(ctx restate.ObjectContext, state ECSClusterState, err error) (ECSClusterOutputs, error) {
	state.Status = types.StatusError
	state.Error = err.Error()
	restate.Set(ctx, drivers.StateKey, state)
	return ECSClusterOutputs{}, err
}

func (d *ECSClusterDriver) scheduleReconcile(ctx restate.ObjectContext, state *ECSClusterState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileDelayFor(ServiceName, restate.Key(ctx))))
}

func (d *ECSClusterDriver) apiForAccount(ctx restate.ObjectContext, account string) (ECSClusterAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("ECSClusterDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve ECSCluster account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func (d *ECSClusterDriver) observeCluster(ctx restate.ObjectContext, api ECSClusterAPI, name string) (ObservedState, bool, error) {
	result, err := restate.Run(ctx, func(rc restate.RunContext) (struct {
		Observed ObservedState
		Found    bool
	}, error) {
		obs, ok, runErr := api.DescribeCluster(rc, name)
		if runErr != nil {
			if IsNotFound(runErr) {
				return struct {
					Observed ObservedState
					Found    bool
				}{}, nil
			}
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
		return ObservedState{}, false, err
	}
	return result.Observed, result.Found, nil
}

// tagDiff computes the tag additions and removals needed to converge the
// observed tag set to the desired one, preserving the praxis managed-key marker.
func tagDiff(desired, observed map[string]string, managedKey string) (map[string]string, []string) {
	want := managedTags(drivers.FilterPraxisTags(desired), managedKey)
	have := managedTags(drivers.FilterPraxisTags(observed), managedKey)
	toAdd := map[string]string{}
	for key, value := range want {
		if current, ok := have[key]; !ok || current != value {
			toAdd[key] = value
		}
	}
	var toRemove []string
	for key := range have {
		if _, ok := want[key]; !ok {
			toRemove = append(toRemove, key)
		}
	}
	sort.Strings(toRemove)
	return toAdd, toRemove
}

func specFromObserved(observed ObservedState) ECSClusterSpec {
	return ECSClusterSpec{
		Name:              observed.Name,
		ContainerInsights: normalizeContainerInsights(observed.ContainerInsights),
		CapacityProviders: append([]string{}, observed.CapacityProviders...),
		Tags:              drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) ECSClusterOutputs {
	return ECSClusterOutputs{
		ARN:    observed.ARN,
		Name:   observed.Name,
		Status: observed.Status,
	}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

func applyDefaults(spec ECSClusterSpec) ECSClusterSpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.ContainerInsights = strings.TrimSpace(spec.ContainerInsights)
	if spec.ContainerInsights == "" {
		spec.ContainerInsights = defaultContainerInsights
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec ECSClusterSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.Name == "" {
		return fmt.Errorf("name is required")
	}
	if !validContainerInsights(spec.ContainerInsights) {
		return fmt.Errorf("containerInsights must be %q or %q", "enabled", "disabled")
	}
	return nil
}

func validContainerInsights(value string) bool {
	return value == "enabled" || value == "disabled"
}

// ClearState clears all Virtual Object state for this resource. Used by the
// Orphan deletion policy to release a resource from management.
func (d *ECSClusterDriver) ClearState(ctx restate.ObjectContext) error {
	drivers.ClearAllState(ctx)
	return nil
}
