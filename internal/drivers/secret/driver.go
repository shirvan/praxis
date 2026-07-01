// Package secret – driver.go
//
// This file implements the Restate Virtual Object handler for AWS Secrets
// Manager secrets. The driver exposes the standard durable handlers:
//   - Provision: create-or-update the resource and persist state
//   - Import:    adopt an existing AWS resource into Praxis management
//   - Delete:    remove the resource from AWS (managed mode only)
//   - Reconcile: periodic drift check + auto-correction (managed mode)
//   - GetStatus / GetOutputs / GetInputs: read-only shared handlers
//
// All mutating AWS calls are wrapped in restate.Run for durable execution,
// and reconciliation is self-scheduled via delayed Restate messages.
package secret

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

// SecretsManagerSecretDriver is the Restate Virtual Object handler for AWS
// Secrets Manager secrets. It holds an auth client (for cross-account
// credential resolution) and an API factory (swappable for testing).
type SecretsManagerSecretDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) SecretsManagerSecretAPI
}

// NewSecretsManagerSecretDriver creates a SecretsManagerSecret driver wired to
// the given auth client. It uses the default AWS SDK client factory.
func NewSecretsManagerSecretDriver(auth authservice.AuthClient) *SecretsManagerSecretDriver {
	return NewSecretsManagerSecretDriverWithFactory(auth, func(cfg aws.Config) SecretsManagerSecretAPI {
		return NewSecretsManagerSecretAPI(awsclient.NewSecretsManagerClient(cfg))
	})
}

// NewSecretsManagerSecretDriverWithFactory creates a SecretsManagerSecret driver
// with a custom API factory, primarily used in tests to inject mock AWS clients.
func NewSecretsManagerSecretDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) SecretsManagerSecretAPI) *SecretsManagerSecretDriver {
	if factory == nil {
		factory = func(cfg aws.Config) SecretsManagerSecretAPI {
			return NewSecretsManagerSecretAPI(awsclient.NewSecretsManagerClient(cfg))
		}
	}
	return &SecretsManagerSecretDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object service name for registration.
func (d *SecretsManagerSecretDriver) ServiceName() string {
	return ServiceName
}

// Provision creates or updates a secret. It validates the spec, checks for an
// existing secret, and either creates a new one or converges diverged fields on
// the existing one. State is persisted in Restate K/V after every step.
func (d *SecretsManagerSecretDriver) Provision(ctx restate.ObjectContext, spec SecretsManagerSecretSpec) (SecretsManagerSecretOutputs, error) {
	ctx.Log().Info("provisioning Secrets Manager secret", "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return SecretsManagerSecretOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	spec.Region = region
	spec.ManagedKey = restate.Key(ctx)
	if err := validateSpec(spec); err != nil {
		return SecretsManagerSecretOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[SecretsManagerSecretState](ctx, drivers.StateKey)
	if err != nil {
		return SecretsManagerSecretOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	observed, found, err := d.observeSecret(ctx, api, spec.Name)
	if err != nil {
		return d.fail(ctx, state, err)
	}

	if !found {
		_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			_, runErr := api.CreateSecret(rc, spec)
			if runErr != nil {
				if IsAlreadyExists(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 409)
				}
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				if IsLimitExceeded(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 409)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return d.fail(ctx, state, err)
		}
		observed, found, err = d.observeSecret(ctx, api, spec.Name)
		if err != nil {
			return d.fail(ctx, state, err)
		}
		if !found {
			return d.fail(ctx, state, fmt.Errorf("secret %s was not found after creation", spec.Name))
		}
	}

	if err := d.convergeMutableFields(ctx, api, spec, observed); err != nil {
		return d.fail(ctx, state, err)
	}

	observed, found, err = d.observeSecret(ctx, api, spec.Name)
	if err != nil {
		return d.fail(ctx, state, err)
	}
	if !found {
		return d.fail(ctx, state, fmt.Errorf("secret %s disappeared during provisioning", spec.Name))
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

// Import adopts an existing secret into Praxis management. It reads the current
// configuration from AWS, synthesizes a spec from the observed state, and
// stores it. Default import mode is Observed (read-only); users can re-import
// with --mode managed to enable writes.
func (d *SecretsManagerSecretDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (SecretsManagerSecretOutputs, error) {
	ctx.Log().Info("importing Secrets Manager secret", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return SecretsManagerSecretOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[SecretsManagerSecretState](ctx, drivers.StateKey)
	if err != nil {
		return SecretsManagerSecretOutputs{}, err
	}
	state.Generation++
	observed, found, err := d.observeSecret(ctx, api, ref.ResourceID)
	if err != nil {
		return SecretsManagerSecretOutputs{}, err
	}
	if !found {
		return SecretsManagerSecretOutputs{}, restate.TerminalError(fmt.Errorf("import failed: secret %s does not exist", ref.ResourceID), 404)
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

// Delete removes the secret from AWS. It is blocked for resources in Observed
// mode. The method handles not-found gracefully (idempotent delete) and sets
// the final state to StatusDeleted.
func (d *SecretsManagerSecretDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting Secrets Manager secret", "key", restate.Key(ctx))
	state, err := restate.Get[SecretsManagerSecretState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete secret %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.Name), 409)
	}
	if state.Outputs.Name == "" {
		restate.Set(ctx, drivers.StateKey, SecretsManagerSecretState{Status: types.StatusDeleted})
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
		runErr := api.DeleteSecret(rc, state.Outputs.Name)
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
	restate.Set(ctx, drivers.StateKey, SecretsManagerSecretState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-check handler. It re-reads the resource from
// AWS, compares against desired state, and auto-corrects drift when in Managed
// mode. In Observed mode it only reports drift. External deletions are detected
// and flagged as errors. The handler self-schedules via a delayed Restate
// message.
func (d *SecretsManagerSecretDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[SecretsManagerSecretState](ctx, drivers.StateKey)
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
	observed, found, err := d.observeSecret(ctx, api, state.Outputs.Name)
	if err != nil {
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}
	if !found {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("secret %s was deleted externally", state.Outputs.Name)
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
func (d *SecretsManagerSecretDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[SecretsManagerSecretState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a shared (read-only) handler that returns the provisioned resource outputs.
func (d *SecretsManagerSecretDriver) GetOutputs(ctx restate.ObjectSharedContext) (SecretsManagerSecretOutputs, error) {
	state, err := restate.Get[SecretsManagerSecretState](ctx, drivers.StateKey)
	if err != nil {
		return SecretsManagerSecretOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *SecretsManagerSecretDriver) GetInputs(ctx restate.ObjectSharedContext) (SecretsManagerSecretSpec, error) {
	state, err := restate.Get[SecretsManagerSecretState](ctx, drivers.StateKey)
	if err != nil {
		return SecretsManagerSecretSpec{}, err
	}
	return state.Desired, nil
}

func (d *SecretsManagerSecretDriver) convergeMutableFields(ctx restate.ObjectContext, api SecretsManagerSecretAPI, spec SecretsManagerSecretSpec, observed ObservedState) error {
	if metadataDrift(spec, observed) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.UpdateSecret(rc, spec.Name, spec.Description, spec.KmsKeyID)
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
	if spec.SecretString != observed.SecretString {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.PutSecretValue(rc, spec.Name, spec.SecretString)
			if runErr != nil {
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				if IsLimitExceeded(runErr) {
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
			return restate.Void{}, api.RemoveTags(rc, spec.Name, toRemove)
		})
		if err != nil {
			return err
		}
	}
	if len(toAdd) > 0 {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.AddTags(rc, spec.Name, toAdd)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// metadataDrift reports whether the description or KMS key converged via
// UpdateSecret has diverged from the observed state.
func metadataDrift(spec SecretsManagerSecretSpec, observed ObservedState) bool {
	if strings.TrimSpace(spec.Description) != strings.TrimSpace(observed.Description) {
		return true
	}
	return !kmsKeyMatch(spec.KmsKeyID, observed.KmsKeyID)
}

func (d *SecretsManagerSecretDriver) fail(ctx restate.ObjectContext, state SecretsManagerSecretState, err error) (SecretsManagerSecretOutputs, error) {
	state.Status = types.StatusError
	state.Error = err.Error()
	restate.Set(ctx, drivers.StateKey, state)
	return SecretsManagerSecretOutputs{}, err
}

func (d *SecretsManagerSecretDriver) scheduleReconcile(ctx restate.ObjectContext, state *SecretsManagerSecretState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileIntervalForKind(ServiceName)))
}

func (d *SecretsManagerSecretDriver) apiForAccount(ctx restate.ObjectContext, account string) (SecretsManagerSecretAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("SecretsManagerSecretDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve SecretsManagerSecret account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func (d *SecretsManagerSecretDriver) observeSecret(ctx restate.ObjectContext, api SecretsManagerSecretAPI, name string) (ObservedState, bool, error) {
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (struct {
		Observed ObservedState
		Found    bool
	}, error) {
		obs, ok, runErr := api.DescribeSecret(rc, name)
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
		if !ok {
			return struct {
				Observed ObservedState
				Found    bool
			}{}, nil
		}
		return struct {
			Observed ObservedState
			Found    bool
		}{Observed: obs, Found: true}, nil
	})
	if err != nil {
		return ObservedState{}, false, err
	}
	return observed.Observed, observed.Found, nil
}

func specFromObserved(observed ObservedState) SecretsManagerSecretSpec {
	kmsKeyID := observed.KmsKeyID
	if kmsKeyID == "alias/aws/secretsmanager" {
		kmsKeyID = ""
	}
	return SecretsManagerSecretSpec{
		Name:         observed.Name,
		Description:  observed.Description,
		KmsKeyID:     kmsKeyID,
		SecretString: observed.SecretString,
		Tags:         drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) SecretsManagerSecretOutputs {
	return SecretsManagerSecretOutputs{
		ARN:       observed.ARN,
		Name:      observed.Name,
		VersionID: observed.VersionID,
	}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

func applyDefaults(spec SecretsManagerSecretSpec) SecretsManagerSecretSpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec SecretsManagerSecretSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if spec.SecretString == "" {
		return fmt.Errorf("secretString is required")
	}
	return nil
}

// ClearState clears all Virtual Object state for this resource.
// Used by the Orphan deletion policy to release a resource from management.
func (d *SecretsManagerSecretDriver) ClearState(ctx restate.ObjectContext) error {
	drivers.ClearAllState(ctx)
	return nil
}
