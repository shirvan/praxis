// Package dynamodbtable – driver.go
//
// This file implements the Restate Virtual Object handler for AWS DynamoDB
// tables. The driver exposes durable handlers:
//   - Provision: create-or-converge the table and persist state
//   - Import:    adopt an existing AWS table into Praxis management
//   - Delete:    remove the table from AWS (managed mode only)
//   - Reconcile: periodic drift check + auto-correction (managed mode)
//   - GetStatus / GetOutputs / GetInputs: read-only shared handlers
//
// All mutating AWS calls are wrapped in restate.Run for durable execution,
// and reconciliation is self-scheduled via delayed Restate messages.
package dynamodbtable

import (
	"fmt"
	"maps"
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

// DynamoDBTableDriver is the Restate Virtual Object handler for AWS DynamoDB
// tables. It holds an auth client (for cross-account credential resolution) and
// an API factory (swappable for testing).
type DynamoDBTableDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) DynamoDBTableAPI
}

// NewDynamoDBTableDriver creates a DynamoDBTable driver wired to the given auth
// client. It uses the default AWS SDK client factory.
func NewDynamoDBTableDriver(auth authservice.AuthClient) *DynamoDBTableDriver {
	return NewDynamoDBTableDriverWithFactory(auth, nil)
}

// NewDynamoDBTableDriverWithFactory creates a DynamoDBTable driver with a custom
// API factory, primarily used in tests to inject mock AWS clients.
func NewDynamoDBTableDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) DynamoDBTableAPI) *DynamoDBTableDriver {
	if factory == nil {
		factory = func(cfg aws.Config) DynamoDBTableAPI {
			return NewDynamoDBTableAPI(awsclient.NewDynamoDBClient(cfg))
		}
	}
	return &DynamoDBTableDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object service name for registration.
func (d *DynamoDBTableDriver) ServiceName() string {
	return ServiceName
}

// Provision creates or converges a DynamoDB table. It validates the spec, checks
// for an existing table, and either creates a new one or converges mutable fields
// on the existing one. State is persisted in Restate K/V after each step.
func (d *DynamoDBTableDriver) Provision(ctx restate.ObjectContext, spec DynamoDBTableSpec) (DynamoDBTableOutputs, error) {
	ctx.Log().Info("provisioning DynamoDB table", "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return DynamoDBTableOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	spec.Region = region
	spec.ManagedKey = restate.Key(ctx)
	if err := validateSpec(spec); err != nil {
		return DynamoDBTableOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[DynamoDBTableState](ctx, drivers.StateKey)
	if err != nil {
		return DynamoDBTableOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	observed, found, err := d.observeTable(ctx, api, spec.Name)
	if err != nil {
		return d.failProvision(ctx, state, err)
	}

	if !found {
		created, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.CreateTable(rc, spec)
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

	if err := d.convergeMutableFields(ctx, api, spec, observed); err != nil {
		return d.failProvision(ctx, state, err)
	}

	observed, found, err = d.observeTable(ctx, api, spec.Name)
	if err != nil {
		return d.failProvision(ctx, state, err)
	}
	if !found {
		return d.failProvision(ctx, state, fmt.Errorf("table %s disappeared during provisioning", spec.Name))
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

// Import adopts an existing DynamoDB table into Praxis management. It reads the
// current configuration from AWS, synthesizes a spec from the observed state,
// and stores it. Default import mode is Observed (read-only); users can re-import
// with --mode managed to enable writes.
func (d *DynamoDBTableDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (DynamoDBTableOutputs, error) {
	ctx.Log().Info("importing DynamoDB table", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return DynamoDBTableOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[DynamoDBTableState](ctx, drivers.StateKey)
	if err != nil {
		return DynamoDBTableOutputs{}, err
	}
	state.Generation++
	observed, found, err := d.observeTable(ctx, api, ref.ResourceID)
	if err != nil {
		return DynamoDBTableOutputs{}, err
	}
	if !found {
		return DynamoDBTableOutputs{}, restate.TerminalError(fmt.Errorf("import failed: table %s does not exist", ref.ResourceID), 404)
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

// Delete removes the DynamoDB table from AWS. It is blocked for resources in
// Observed mode. The method handles not-found gracefully (idempotent delete) and
// sets the final state to StatusDeleted.
func (d *DynamoDBTableDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting DynamoDB table", "key", restate.Key(ctx))
	state, err := restate.Get[DynamoDBTableState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete table %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.Name), 409)
	}
	if state.Outputs.Name == "" {
		restate.Set(ctx, drivers.StateKey, DynamoDBTableState{Status: types.StatusDeleted})
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
		runErr := api.DeleteTable(rc, state.Outputs.Name)
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
	restate.Set(ctx, drivers.StateKey, DynamoDBTableState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-check handler. It re-reads the table from AWS,
// compares against desired state, and auto-corrects drift when in Managed mode.
// In Observed mode it only reports drift. External deletions are detected and
// flagged as errors. The handler self-schedules via a delayed message.
func (d *DynamoDBTableDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[DynamoDBTableState](ctx, drivers.StateKey)
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
	observed, found, err := d.observeTable(ctx, api, state.Outputs.Name)
	if err != nil {
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}
	if !found {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("table %s was deleted externally", state.Outputs.Name)
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
func (d *DynamoDBTableDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[DynamoDBTableState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a shared (read-only) handler that returns the provisioned resource outputs.
func (d *DynamoDBTableDriver) GetOutputs(ctx restate.ObjectSharedContext) (DynamoDBTableOutputs, error) {
	state, err := restate.Get[DynamoDBTableState](ctx, drivers.StateKey)
	if err != nil {
		return DynamoDBTableOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *DynamoDBTableDriver) GetInputs(ctx restate.ObjectSharedContext) (DynamoDBTableSpec, error) {
	state, err := restate.Get[DynamoDBTableState](ctx, drivers.StateKey)
	if err != nil {
		return DynamoDBTableSpec{}, err
	}
	return state.Desired, nil
}

// convergeMutableFields brings an existing table in line with the desired spec:
// billing mode / provisioned throughput and tags. Immutable fields (the primary
// key schema) are never touched here.
func (d *DynamoDBTableDriver) convergeMutableFields(ctx restate.ObjectContext, api DynamoDBTableAPI, spec DynamoDBTableSpec, observed ObservedState) error {
	if configDrift(spec, observed) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.UpdateTable(rc, spec)
			if runErr != nil {
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				if IsConflict(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 409)
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

func (d *DynamoDBTableDriver) failProvision(ctx restate.ObjectContext, state DynamoDBTableState, err error) (DynamoDBTableOutputs, error) {
	state.Status = types.StatusError
	state.Error = err.Error()
	restate.Set(ctx, drivers.StateKey, state)
	return DynamoDBTableOutputs{}, err
}

func (d *DynamoDBTableDriver) scheduleReconcile(ctx restate.ObjectContext, state *DynamoDBTableState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileIntervalForKind(ServiceName)))
}

func (d *DynamoDBTableDriver) apiForAccount(ctx restate.ObjectContext, account string) (DynamoDBTableAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("DynamoDBTableDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve DynamoDBTable account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func (d *DynamoDBTableDriver) observeTable(ctx restate.ObjectContext, api DynamoDBTableAPI, name string) (ObservedState, bool, error) {
	result, err := restate.Run(ctx, func(rc restate.RunContext) (struct {
		Observed ObservedState
		Found    bool
	}, error) {
		obs, ok, runErr := api.DescribeTable(rc, name)
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

// tagDiff computes the tag additions and removals needed to converge the observed
// tag set to the desired one, preserving the praxis managed-key marker.
func tagDiff(desired, observed map[string]string, managedKey string) (map[string]string, []string) {
	want := mergeManagedKey(drivers.FilterPraxisTags(desired), managedKey)
	have := mergeManagedKey(drivers.FilterPraxisTags(observed), managedKey)
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

// mergeManagedKey returns a copy of tags with the praxis managed-key marker
// added, mirroring what create-time tagging stamps so the marker never surfaces
// as drift.
func mergeManagedKey(tags map[string]string, managedKey string) map[string]string {
	out := make(map[string]string, len(tags)+1)
	maps.Copy(out, tags)
	if managedKey != "" {
		out["praxis:managed-key"] = managedKey
	}
	return out
}

func specFromObserved(observed ObservedState) DynamoDBTableSpec {
	return DynamoDBTableSpec{
		Name:          observed.Name,
		BillingMode:   billingModeOrDefault(observed.BillingMode),
		HashKey:       observed.HashKey,
		HashKeyType:   observed.HashKeyType,
		RangeKey:      observed.RangeKey,
		RangeKeyType:  observed.RangeKeyType,
		ReadCapacity:  observed.ReadCapacity,
		WriteCapacity: observed.WriteCapacity,
		Tags:          drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) DynamoDBTableOutputs {
	return DynamoDBTableOutputs{
		ARN:       observed.ARN,
		Name:      observed.Name,
		Status:    observed.Status,
		ItemCount: observed.ItemCount,
	}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

func applyDefaults(spec DynamoDBTableSpec) DynamoDBTableSpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.BillingMode = billingModeOrDefault(strings.TrimSpace(spec.BillingMode))
	spec.HashKey = strings.TrimSpace(spec.HashKey)
	spec.HashKeyType = keyTypeOrDefault(strings.TrimSpace(spec.HashKeyType))
	spec.RangeKey = strings.TrimSpace(spec.RangeKey)
	if spec.RangeKey != "" {
		spec.RangeKeyType = keyTypeOrDefault(strings.TrimSpace(spec.RangeKeyType))
	} else {
		spec.RangeKeyType = ""
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec DynamoDBTableSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.Name == "" {
		return fmt.Errorf("name is required")
	}
	if spec.HashKey == "" {
		return fmt.Errorf("hashKey is required")
	}
	if !validKeyType(spec.HashKeyType) {
		return fmt.Errorf("hashKeyType must be one of S, N, B")
	}
	if spec.RangeKey != "" && !validKeyType(spec.RangeKeyType) {
		return fmt.Errorf("rangeKeyType must be one of S, N, B")
	}
	if spec.BillingMode != BillingModePayPerRequest && spec.BillingMode != BillingModeProvisioned {
		return fmt.Errorf("billingMode must be one of %s, %s", BillingModePayPerRequest, BillingModeProvisioned)
	}
	return nil
}

func validKeyType(t string) bool {
	return t == "S" || t == "N" || t == "B"
}

// ClearState clears all Virtual Object state for this resource. Used by the
// Orphan deletion policy to release a resource from management.
func (d *DynamoDBTableDriver) ClearState(ctx restate.ObjectContext) error {
	drivers.ClearAllState(ctx)
	return nil
}
