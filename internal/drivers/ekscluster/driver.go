// Package ekscluster – driver.go
//
// This file implements the Restate Virtual Object handler for AWS EKS clusters.
// The driver exposes durable handlers:
//   - Provision: create-or-converge the cluster and persist state
//   - Import:    adopt an existing AWS cluster into Praxis management
//   - Delete:    remove the cluster from AWS (managed mode only)
//   - Reconcile: periodic drift check + auto-correction (managed mode)
//   - GetStatus / GetOutputs / GetInputs: read-only shared handlers
//
// All mutating AWS calls are wrapped in restate.Run for durable execution,
// and reconciliation is self-scheduled via delayed Restate messages.
package ekscluster

import (
	"fmt"
	"slices"
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

// EKSClusterDriver is the Restate Virtual Object handler for AWS EKS clusters.
// It holds an auth client (for cross-account credential resolution) and an API
// factory (swappable for testing).
type EKSClusterDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) EKSClusterAPI
}

// NewEKSClusterDriver creates an EKSCluster driver wired to the given auth
// client. It uses the default AWS SDK client factory.
func NewEKSClusterDriver(auth authservice.AuthClient) *EKSClusterDriver {
	return NewEKSClusterDriverWithFactory(auth, nil)
}

// NewEKSClusterDriverWithFactory creates an EKSCluster driver with a custom API
// factory, primarily used in tests to inject mock AWS clients.
func NewEKSClusterDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) EKSClusterAPI) *EKSClusterDriver {
	if factory == nil {
		factory = func(cfg aws.Config) EKSClusterAPI {
			return NewEKSClusterAPI(awsclient.NewEKSClient(cfg))
		}
	}
	return &EKSClusterDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object service name for registration.
func (d *EKSClusterDriver) ServiceName() string {
	return ServiceName
}

// Provision creates or converges an EKS cluster. It validates the spec, checks
// for an existing cluster, and either creates a new one or converges mutable
// fields on the existing one. State is persisted in Restate K/V after each step.
func (d *EKSClusterDriver) Provision(ctx restate.ObjectContext, spec EKSClusterSpec) (EKSClusterOutputs, error) {
	ctx.Log().Info("provisioning EKS cluster", "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return EKSClusterOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	spec.Region = region
	spec.ManagedKey = restate.Key(ctx)
	if err := validateSpec(spec); err != nil {
		return EKSClusterOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[EKSClusterState](ctx, drivers.StateKey)
	if err != nil {
		return EKSClusterOutputs{}, err
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
				if IsConflict(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 409)
				}
				if IsInvalidParam(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 400)
				}
				if IsLimitExceeded(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 409)
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

	// A freshly created cluster is CREATING for 10–15 minutes; an existing one
	// may be UPDATING. Wait durably for ACTIVE before converging mutable fields
	// (EKS rejects UpdateClusterConfig on a non-ACTIVE cluster with
	// ResourceInUseException) and before reporting the resource Ready, so that
	// dependents (node groups, add-ons) don't dispatch against a cluster that
	// doesn't exist yet. An already-ACTIVE cluster skips the wait entirely, so
	// the no-op re-provision fast path costs no extra describes.
	if !strings.EqualFold(observed.Status, "ACTIVE") {
		observed, err = d.waitForActive(ctx, api, spec.Name)
		if err != nil {
			return d.failProvision(ctx, state, err)
		}
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

// Import adopts an existing EKS cluster into Praxis management. It reads the
// current configuration from AWS, synthesizes a spec from the observed state,
// and stores it. Default import mode is Observed (read-only); users can
// re-import with --mode managed to enable writes.
func (d *EKSClusterDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (EKSClusterOutputs, error) {
	ctx.Log().Info("importing EKS cluster", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return EKSClusterOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[EKSClusterState](ctx, drivers.StateKey)
	if err != nil {
		return EKSClusterOutputs{}, err
	}
	state.Generation++
	observed, found, err := d.observeCluster(ctx, api, ref.ResourceID)
	if err != nil {
		return EKSClusterOutputs{}, err
	}
	if !found {
		return EKSClusterOutputs{}, restate.TerminalError(fmt.Errorf("import failed: cluster %s does not exist", ref.ResourceID), 404)
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

// Delete removes the EKS cluster from AWS. It is blocked for resources in
// Observed mode. The method handles not-found gracefully (idempotent delete)
// and sets the final state to StatusDeleted.
func (d *EKSClusterDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting EKS cluster", "key", restate.Key(ctx))
	state, err := restate.Get[EKSClusterState](ctx, drivers.StateKey)
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
		restate.Set(ctx, drivers.StateKey, EKSClusterState{Status: types.StatusDeleted})
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
	restate.Set(ctx, drivers.StateKey, EKSClusterState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-check handler. It re-reads the cluster from
// AWS, compares against desired state, and auto-corrects drift when in Managed
// mode. In Observed mode it only reports drift. External deletions are detected
// and flagged as errors. The handler self-schedules via a delayed message.
func (d *EKSClusterDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[EKSClusterState](ctx, drivers.StateKey)
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
		// EKS allows only one in-flight update and rejects UpdateClusterConfig/
		// Version on a non-ACTIVE cluster with ResourceInUseException. Although
		// the update path now treats that response as retryable, avoiding the call
		// while status is already known to be transitional saves needless retries.
		if !strings.EqualFold(observed.Status, "ACTIVE") {
			ctx.Log().Info("deferring drift correction: cluster not ACTIVE",
				"cluster", state.Outputs.Name, "status", observed.Status)
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true}, nil
		}
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
func (d *EKSClusterDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[EKSClusterState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a shared (read-only) handler that returns the provisioned resource outputs.
func (d *EKSClusterDriver) GetOutputs(ctx restate.ObjectSharedContext) (EKSClusterOutputs, error) {
	state, err := restate.Get[EKSClusterState](ctx, drivers.StateKey)
	if err != nil {
		return EKSClusterOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *EKSClusterDriver) GetInputs(ctx restate.ObjectSharedContext) (EKSClusterSpec, error) {
	state, err := restate.Get[EKSClusterState](ctx, drivers.StateKey)
	if err != nil {
		return EKSClusterSpec{}, err
	}
	return state.Desired, nil
}

// convergeMutableFields brings an existing cluster in line with the desired
// spec: version upgrade, endpoint/logging configuration, and tags. Immutable
// fields (role, subnets, security groups) are never touched here.
func (d *EKSClusterDriver) convergeMutableFields(ctx restate.ObjectContext, api EKSClusterAPI, spec EKSClusterSpec, observed ObservedState) error {
	if versionDrift(spec, observed) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.UpdateClusterVersion(rc, spec.Name, spec.Version)
			if runErr != nil {
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				// ResourceInUse during update means another EKS operation is
				// still active. Retrying is safe; only create treats it as a
				// terminal conflict with an already-existing resource.
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return err
		}
	}

	if endpointOrLoggingDrift(spec, observed) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.UpdateClusterConfig(rc, spec)
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

// endpointOrLoggingDrift reports whether any field converged via
// UpdateClusterConfig has diverged.
func endpointOrLoggingDrift(spec EKSClusterSpec, observed ObservedState) bool {
	if spec.EndpointPublicAccess != observed.EndpointPublicAccess {
		return true
	}
	if spec.EndpointPrivateAccess != observed.EndpointPrivateAccess {
		return true
	}
	if spec.EndpointPublicAccess && !stringSetEqual(normalizePublicCidrs(spec.PublicAccessCidrs), normalizePublicCidrs(observed.PublicAccessCidrs)) {
		return true
	}
	return !stringSetEqual(spec.EnabledLoggingTypes, observed.EnabledLoggingTypes)
}

func (d *EKSClusterDriver) failProvision(ctx restate.ObjectContext, state EKSClusterState, err error) (EKSClusterOutputs, error) {
	state.Status = types.StatusError
	state.Error = err.Error()
	restate.Set(ctx, drivers.StateKey, state)
	return EKSClusterOutputs{}, err
}

func (d *EKSClusterDriver) scheduleReconcile(ctx restate.ObjectContext, state *EKSClusterState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileDelayFor(ServiceName, restate.Key(ctx))))
}

func (d *EKSClusterDriver) apiForAccount(ctx restate.ObjectContext, account string) (EKSClusterAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("EKSClusterDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve EKSCluster account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func (d *EKSClusterDriver) observeCluster(ctx restate.ObjectContext, api EKSClusterAPI, name string) (ObservedState, bool, error) {
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

const (
	// eksReadyPollInterval is the delay between durable readiness checks.
	eksReadyPollInterval = 15 * time.Second
	// eksReadyMaxAttempts bounds the wait (~25 minutes) so a cluster wedged in
	// CREATING can't loop forever growing the journal. Control-plane creation
	// typically takes 10–15 minutes; the budget needs real headroom above the
	// typical worst case, or a slightly slow create terminally fails the whole
	// deployment while AWS finishes the cluster anyway.
	eksReadyMaxAttempts = 100
)

// waitForActive polls the cluster durably (one DescribeCluster per restate.Run,
// restate.Sleep between attempts) until it reports ACTIVE, returning the final
// observed state. A FAILED status or exhausting the attempt budget yields a
// terminal error. Because each poll is its own journaled step, a crash mid-wait
// resumes at the next attempt rather than re-running the whole wait, and the
// object's lock is released between polls.
func (d *EKSClusterDriver) waitForActive(ctx restate.ObjectContext, api EKSClusterAPI, name string) (ObservedState, error) {
	for range eksReadyMaxAttempts {
		observed, found, err := d.observeCluster(ctx, api, name)
		if err != nil {
			return ObservedState{}, err
		}
		if !found {
			// 404 per docs/ERRORS.md: deleted outside Praxis, not a bug.
			return ObservedState{}, restate.TerminalError(fmt.Errorf("cluster %s disappeared while waiting for ACTIVE", name), 404)
		}
		switch strings.ToUpper(observed.Status) {
		case "ACTIVE":
			return observed, nil
		case "FAILED":
			return ObservedState{}, restate.TerminalError(fmt.Errorf("cluster %s entered FAILED state", name), 500)
		}
		if err := restate.Sleep(ctx, eksReadyPollInterval); err != nil {
			return ObservedState{}, err
		}
	}
	return ObservedState{}, restate.TerminalError(
		fmt.Errorf("cluster %s not ACTIVE after %s", name, time.Duration(eksReadyMaxAttempts)*eksReadyPollInterval), 500)
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

func specFromObserved(observed ObservedState) EKSClusterSpec {
	return EKSClusterSpec{
		Name:                  observed.Name,
		RoleArn:               observed.RoleArn,
		SubnetIds:             append([]string{}, observed.SubnetIds...),
		SecurityGroupIds:      append([]string{}, observed.SecurityGroupIds...),
		Version:               observed.Version,
		EndpointPublicAccess:  observed.EndpointPublicAccess,
		EndpointPrivateAccess: observed.EndpointPrivateAccess,
		PublicAccessCidrs:     append([]string{}, observed.PublicAccessCidrs...),
		EnabledLoggingTypes:   append([]string{}, observed.EnabledLoggingTypes...),
		Tags:                  drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) EKSClusterOutputs {
	return EKSClusterOutputs{
		ARN:             observed.ARN,
		Name:            observed.Name,
		Status:          observed.Status,
		Version:         observed.Version,
		PlatformVersion: observed.PlatformVersion,
		Endpoint:        observed.Endpoint,
	}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

func applyDefaults(spec EKSClusterSpec) EKSClusterSpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.RoleArn = strings.TrimSpace(spec.RoleArn)
	spec.Version = strings.TrimSpace(spec.Version)
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec EKSClusterSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.Name == "" {
		return fmt.Errorf("name is required")
	}
	if spec.RoleArn == "" {
		return fmt.Errorf("roleArn is required")
	}
	if len(spec.SubnetIds) < 2 {
		return fmt.Errorf("subnetIds requires at least two subnets in different availability zones")
	}
	for _, t := range spec.EnabledLoggingTypes {
		if !validLogType(t) {
			return fmt.Errorf("enabledLoggingTypes contains invalid log type %q", t)
		}
	}
	return nil
}

func validLogType(t string) bool {
	return slices.Contains(allLogTypes, t)
}

// ClearState clears all Virtual Object state for this resource. Used by the
// Orphan deletion policy to release a resource from management.
func (d *EKSClusterDriver) ClearState(ctx restate.ObjectContext) error {
	drivers.ClearAllState(ctx)
	return nil
}
