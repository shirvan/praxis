// Package kmskey – driver.go
//
// This file implements the Restate Virtual Object handler for AWS KMS keys.
// The driver exposes durable handlers:
//   - Provision: create-or-converge the key + alias and persist state
//   - Import:    adopt an existing AWS key (by alias) into Praxis management
//   - Delete:    remove the alias and schedule key deletion (managed mode only)
//   - Reconcile: periodic drift check + auto-correction (managed mode)
//   - GetStatus / GetOutputs / GetInputs: read-only shared handlers
//
// A KMSKey manages both a KMS key and its alias "alias/<name>" as a unit;
// identity is established by alias lookup. All mutating AWS calls are wrapped in
// restate.Run for durable execution, and reconciliation is self-scheduled via
// delayed Restate messages.
package kmskey

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

const (
	defaultKeyUsage   = "ENCRYPT_DECRYPT"
	defaultKeySpec    = "SYMMETRIC_DEFAULT"
	defaultDeleteWait = int32(30)
	aliasPrefix       = "alias/"
)

// KMSKeyDriver is the Restate Virtual Object handler for AWS KMS keys. It holds
// an auth client (for cross-account credential resolution) and an API factory
// (swappable for testing).
type KMSKeyDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) KMSKeyAPI
}

// NewKMSKeyDriver creates a KMSKey driver wired to the given auth client. It uses
// the default AWS SDK client factory.
func NewKMSKeyDriver(auth authservice.AuthClient) *KMSKeyDriver {
	return NewKMSKeyDriverWithFactory(auth, nil)
}

// NewKMSKeyDriverWithFactory creates a KMSKey driver with a custom API factory,
// primarily used in tests to inject mock AWS clients.
func NewKMSKeyDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) KMSKeyAPI) *KMSKeyDriver {
	if factory == nil {
		factory = func(cfg aws.Config) KMSKeyAPI {
			return NewKMSKeyAPI(awsclient.NewKMSClient(cfg))
		}
	}
	return &KMSKeyDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object service name for registration.
func (d *KMSKeyDriver) ServiceName() string {
	return ServiceName
}

// Provision creates or converges a KMS key and its alias. It validates the spec,
// checks for an existing key via alias lookup, and either creates a new key +
// alias or converges mutable fields on the existing one. State is persisted in
// Restate K/V after each step.
func (d *KMSKeyDriver) Provision(ctx restate.ObjectContext, spec KMSKeySpec) (KMSKeyOutputs, error) {
	ctx.Log().Info("provisioning KMS key", "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return KMSKeyOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	spec.Region = region
	spec.ManagedKey = restate.Key(ctx)
	if err := validateSpec(spec); err != nil {
		return KMSKeyOutputs{}, restate.TerminalError(err, 400)
	}
	alias := aliasFor(spec.Name)

	state, err := restate.Get[KMSKeyState](ctx, drivers.StateKey)
	if err != nil {
		return KMSKeyOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	observed, found, err := d.observeKey(ctx, api, alias)
	if err != nil {
		return d.failProvision(ctx, state, err)
	}

	if !found {
		// CreateKey and CreateAlias are journaled as two separate durable steps
		// so a retry after a successful CreateKey does not orphan a second key.
		keyID, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, _, runErr := api.CreateKey(rc, spec)
			if runErr != nil {
				return "", classifyMutation(runErr)
			}
			return id, nil
		})
		if err != nil {
			return d.failProvision(ctx, state, err)
		}
		_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.CreateAlias(rc, alias, keyID)
			if runErr != nil && !IsConflict(runErr) {
				return restate.Void{}, classifyMutation(runErr)
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return d.failProvision(ctx, state, err)
		}
		observed, found, err = d.observeKey(ctx, api, alias)
		if err != nil {
			return d.failProvision(ctx, state, err)
		}
		if !found {
			return d.failProvision(ctx, state, fmt.Errorf("key %s was not found after creation", alias))
		}
	}

	if err := d.convergeMutableFields(ctx, api, spec, observed); err != nil {
		return d.failProvision(ctx, state, err)
	}

	observed, found, err = d.observeKey(ctx, api, alias)
	if err != nil {
		return d.failProvision(ctx, state, err)
	}
	if !found {
		return d.failProvision(ctx, state, fmt.Errorf("key %s disappeared during provisioning", alias))
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

// Import adopts an existing KMS key (looked up by alias) into Praxis management.
// It reads the current configuration from AWS, synthesizes a spec from the
// observed state, and stores it. Default import mode is Observed (read-only);
// users can re-import with --mode managed to enable writes. The import resource
// ID is the alias short name (without the "alias/" prefix).
func (d *KMSKeyDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (KMSKeyOutputs, error) {
	ctx.Log().Info("importing KMS key", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return KMSKeyOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[KMSKeyState](ctx, drivers.StateKey)
	if err != nil {
		return KMSKeyOutputs{}, err
	}
	state.Generation++
	name := strings.TrimPrefix(strings.TrimSpace(ref.ResourceID), aliasPrefix)
	alias := aliasFor(name)
	observed, found, err := d.observeKey(ctx, api, alias)
	if err != nil {
		return KMSKeyOutputs{}, err
	}
	if !found {
		return KMSKeyOutputs{}, restate.TerminalError(fmt.Errorf("import failed: key %s does not exist", alias), 404)
	}
	spec := specFromObserved(name, observed)
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

// Delete removes the alias and schedules the KMS key for deletion. It is blocked
// for resources in Observed mode. Both the alias delete and the key deletion
// scheduling handle not-found gracefully (idempotent delete). The final state is
// StatusDeleted; no reconcile is scheduled after deletion.
func (d *KMSKeyDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting KMS key", "key", restate.Key(ctx))
	state, err := restate.Get[KMSKeyState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete key %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.AliasName), 409)
	}
	if state.Outputs.KeyID == "" {
		restate.Set(ctx, drivers.StateKey, KMSKeyState{Status: types.StatusDeleted})
		return nil
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}
	window := state.Desired.DeletionWindowInDays
	if window == 0 {
		window = defaultDeleteWait
	}
	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if state.Outputs.AliasName != "" {
			if runErr := api.DeleteAlias(rc, state.Outputs.AliasName); runErr != nil && !IsNotFound(runErr) {
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				return restate.Void{}, runErr
			}
		}
		if runErr := api.ScheduleKeyDeletion(rc, state.Outputs.KeyID, window); runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
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
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}
	restate.Set(ctx, drivers.StateKey, KMSKeyState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-check handler. It re-reads the key from AWS,
// compares against desired state, and auto-corrects drift when in Managed mode.
// In Observed mode it only reports drift. External deletions (alias removed) are
// detected and flagged as errors. The handler self-schedules via a delayed message.
func (d *KMSKeyDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[KMSKeyState](ctx, drivers.StateKey)
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
	if state.Outputs.AliasName == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return types.ReconcileResult{}, err
	}
	observed, found, err := d.observeKey(ctx, api, state.Outputs.AliasName)
	if err != nil {
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}
	if !found {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("key %s was deleted externally", state.Outputs.AliasName)
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
func (d *KMSKeyDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[KMSKeyState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a shared (read-only) handler that returns the provisioned resource outputs.
func (d *KMSKeyDriver) GetOutputs(ctx restate.ObjectSharedContext) (KMSKeyOutputs, error) {
	state, err := restate.Get[KMSKeyState](ctx, drivers.StateKey)
	if err != nil {
		return KMSKeyOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *KMSKeyDriver) GetInputs(ctx restate.ObjectSharedContext) (KMSKeySpec, error) {
	state, err := restate.Get[KMSKeyState](ctx, drivers.StateKey)
	if err != nil {
		return KMSKeySpec{}, err
	}
	return state.Desired, nil
}

// convergeMutableFields brings an existing key in line with the desired spec:
// description, rotation state, and tags. Immutable fields (key usage, key spec)
// are never touched here.
func (d *KMSKeyDriver) convergeMutableFields(ctx restate.ObjectContext, api KMSKeyAPI, spec KMSKeySpec, observed ObservedState) error {
	keyID := observed.KeyID
	if spec.Description != observed.Description {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, classifyMutation(api.UpdateDescription(rc, keyID, spec.Description))
		})
		if err != nil {
			return err
		}
	}

	if spec.EnableKeyRotation != observed.EnableKeyRotation {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if spec.EnableKeyRotation {
				return restate.Void{}, classifyMutation(api.EnableKeyRotation(rc, keyID))
			}
			return restate.Void{}, classifyMutation(api.DisableKeyRotation(rc, keyID))
		})
		if err != nil {
			return err
		}
	}

	toAdd, toRemove := tagDiff(spec.Tags, observed.Tags, spec.ManagedKey)
	if len(toRemove) > 0 {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UntagResource(rc, keyID, toRemove)
		})
		if err != nil {
			return err
		}
	}
	if len(toAdd) > 0 {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.TagResource(rc, keyID, toAdd)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *KMSKeyDriver) failProvision(ctx restate.ObjectContext, state KMSKeyState, err error) (KMSKeyOutputs, error) {
	state.Status = types.StatusError
	state.Error = err.Error()
	restate.Set(ctx, drivers.StateKey, state)
	return KMSKeyOutputs{}, err
}

func (d *KMSKeyDriver) scheduleReconcile(ctx restate.ObjectContext, state *KMSKeyState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileDelayFor(ServiceName, restate.Key(ctx))))
}

func (d *KMSKeyDriver) apiForAccount(ctx restate.ObjectContext, account string) (KMSKeyAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("KMSKeyDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve KMSKey account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func (d *KMSKeyDriver) observeKey(ctx restate.ObjectContext, api KMSKeyAPI, alias string) (ObservedState, bool, error) {
	result, err := restate.Run(ctx, func(rc restate.RunContext) (struct {
		Observed ObservedState
		Found    bool
	}, error) {
		obs, ok, runErr := api.DescribeKey(rc, alias)
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

// classifyMutation maps AWS errors from a mutating KMS call onto terminal Restate
// errors with the right status code, leaving retryable errors untouched.
func classifyMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsConflict(err) {
		return restate.TerminalError(err, 409)
	}
	if IsInvalidParam(err) {
		return restate.TerminalError(err, 400)
	}
	if IsLimitExceeded(err) {
		return restate.TerminalError(err, 409)
	}
	return err
}

func specFromObserved(name string, observed ObservedState) KMSKeySpec {
	return KMSKeySpec{
		Name:                 name,
		Description:          observed.Description,
		KeyUsage:             observed.KeyUsage,
		KeySpec:              observed.KeySpec,
		EnableKeyRotation:    observed.EnableKeyRotation,
		DeletionWindowInDays: defaultDeleteWait,
		Tags:                 drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) KMSKeyOutputs {
	return KMSKeyOutputs{
		ARN:       observed.ARN,
		KeyID:     observed.KeyID,
		AliasName: observed.AliasName,
	}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

// aliasFor derives the full KMS alias ("alias/<name>") from the alias short name,
// tolerating callers that already supplied the prefix.
func aliasFor(name string) string {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, aliasPrefix) {
		return name
	}
	return aliasPrefix + name
}

func applyDefaults(spec KMSKeySpec) KMSKeySpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.Name = strings.TrimPrefix(strings.TrimSpace(spec.Name), aliasPrefix)
	spec.Description = strings.TrimSpace(spec.Description)
	spec.KeyUsage = strings.TrimSpace(spec.KeyUsage)
	if spec.KeyUsage == "" {
		spec.KeyUsage = defaultKeyUsage
	}
	spec.KeySpec = strings.TrimSpace(spec.KeySpec)
	if spec.KeySpec == "" {
		spec.KeySpec = defaultKeySpec
	}
	if spec.DeletionWindowInDays == 0 {
		spec.DeletionWindowInDays = defaultDeleteWait
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec KMSKeySpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.Name == "" {
		return fmt.Errorf("name is required")
	}
	switch spec.KeyUsage {
	case "ENCRYPT_DECRYPT", "SIGN_VERIFY", "GENERATE_VERIFY_MAC":
	default:
		return fmt.Errorf("keyUsage must be ENCRYPT_DECRYPT, SIGN_VERIFY, or GENERATE_VERIFY_MAC")
	}
	if spec.KeySpec == "" {
		return fmt.Errorf("keySpec is required")
	}
	if spec.DeletionWindowInDays < 7 || spec.DeletionWindowInDays > 30 {
		return fmt.Errorf("deletionWindowInDays must be between 7 and 30")
	}
	return nil
}

// ClearState clears all Virtual Object state for this resource. Used by the
// Orphan deletion policy to release a resource from management.
func (d *KMSKeyDriver) ClearState(ctx restate.ObjectContext) error {
	drivers.ClearAllState(ctx)
	return nil
}
