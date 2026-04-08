// Package ecrrepo – driver.go
//
// This file implements the Restate Virtual Object handler for AWS ECR Repository.
// The driver exposes five durable handlers:
//   - Provision: create-or-update the resource and persist state
//   - Import:    adopt an existing AWS resource into Praxis management
//   - Delete:    remove the resource from AWS (managed mode only)
//   - Reconcile: periodic drift check + auto-correction (managed mode)
//   - GetStatus / GetOutputs: read-only shared handlers for status queries
//
// All mutating AWS calls are wrapped in restate.Run for durable execution,
// and reconciliation is self-scheduled via delayed Restate messages.
package ecrrepo

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

// ECRRepositoryDriver is the Restate Virtual Object handler for AWS ECR Repository.
// It holds an auth client (for cross-account credential resolution)
// and an API factory (swappable for testing).
type ECRRepositoryDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) RepositoryAPI
}

// NewECRRepositoryDriver creates a ECRRepository driver wired to the given
// auth client. It uses the default AWS SDK client factory.
func NewECRRepositoryDriver(auth authservice.AuthClient) *ECRRepositoryDriver {
	return NewECRRepositoryDriverWithFactory(auth, func(cfg aws.Config) RepositoryAPI {
		return NewRepositoryAPI(awsclient.NewECRClient(cfg))
	})
}

// NewECRRepositoryDriverWithFactory creates a ECRRepository driver with a custom API
// factory, primarily used in tests to inject mock AWS clients.
func NewECRRepositoryDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) RepositoryAPI) *ECRRepositoryDriver {
	if factory == nil {
		factory = func(cfg aws.Config) RepositoryAPI { return NewRepositoryAPI(awsclient.NewECRClient(cfg)) }
	}
	return &ECRRepositoryDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object service name for registration.
func (d *ECRRepositoryDriver) ServiceName() string { return ServiceName }

// Provision creates or updates a AWS ECR Repository. It validates the spec,
// checks for an existing resource (by ARN or name), detects immutable-field
// conflicts, and either creates a new resource or corrects drift on the
// existing one. State is persisted in Restate K/V after every step.
func (d *ECRRepositoryDriver) Provision(ctx restate.ObjectContext, spec ECRRepositorySpec) (ECRRepositoryOutputs, error) {
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return ECRRepositoryOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	if spec.Region == "" {
		spec.Region = region
	}
	if err := validateProvisionSpec(spec); err != nil {
		return ECRRepositoryOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[ECRRepositoryState](ctx, drivers.StateKey)
	if err != nil {
		return ECRRepositoryOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	observed, found, err := d.describeExisting(ctx, api, spec.RepositoryName)
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return ECRRepositoryOutputs{}, err
	}

	if !found {
		observed, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			created, runErr := api.CreateRepository(rc, spec)
			if runErr != nil {
				if IsInvalidParameter(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 400)
				}
				if IsConflict(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 409)
				}
				return ObservedState{}, runErr
			}
			return created, nil
		})
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return ECRRepositoryOutputs{}, err
		}
	} else {
		if !encryptionEqual(spec.EncryptionConfiguration, observed.EncryptionConfiguration) {
			return ECRRepositoryOutputs{}, restate.TerminalError(fmt.Errorf("encryptionConfiguration is immutable; delete and recreate the repository to change it"), 409)
		}
		if err := d.applyMutableUpdates(ctx, api, spec, observed); err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return ECRRepositoryOutputs{}, err
		}
		observed, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.DescribeRepository(rc, spec.RepositoryName)
		})
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return ECRRepositoryOutputs{}, err
		}
	}

	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Import adopts an existing AWS ECR Repository into Praxis management.
// It reads the current configuration from AWS, synthesizes a spec from
// the observed state, and stores it. Default import mode is Observed
// (read-only); users can re-import with --mode managed to enable writes.
func (d *ECRRepositoryDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (ECRRepositoryOutputs, error) {
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return ECRRepositoryOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[ECRRepositoryState](ctx, drivers.StateKey)
	if err != nil {
		return ECRRepositoryOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.DescribeRepository(rc, ref.ResourceID)
	})
	if err != nil {
		if IsNotFound(err) {
			return ECRRepositoryOutputs{}, restate.TerminalError(fmt.Errorf("import failed: ECR repository %s does not exist", ref.ResourceID), 404)
		}
		return ECRRepositoryOutputs{}, err
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	if spec.Region == "" {
		spec.Region = region
	}
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Mode = drivers.DefaultMode(ref.Mode)
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Delete removes the AWS ECR Repository from AWS. It is blocked for
// resources in Observed mode. The method handles not-found gracefully
// (idempotent delete) and sets the final state to StatusDeleted.
func (d *ECRRepositoryDriver) Delete(ctx restate.ObjectContext) error {
	state, err := restate.Get[ECRRepositoryState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete ECR repository %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.RepositoryName), 409)
	}
	name := state.Desired.RepositoryName
	if name == "" {
		name = state.Outputs.RepositoryName
	}
	if name == "" {
		restate.Set(ctx, drivers.StateKey, ECRRepositoryState{Status: types.StatusDeleted})
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
		deleteErr := api.DeleteRepository(rc, name, state.Desired.ForceDelete)
		if deleteErr != nil {
			if IsRepositoryNotEmpty(deleteErr) {
				return restate.Void{}, restate.TerminalError(deleteErr, 409)
			}
			if IsNotFound(deleteErr) {
				return restate.Void{}, nil
			}
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
	restate.Set(ctx, drivers.StateKey, ECRRepositoryState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-check handler. It re-reads the
// resource from AWS, compares against desired state, and auto-corrects
// drift when in Managed mode. In Observed mode it only reports drift.
// External deletions are detected and flagged as errors.
// The handler self-schedules via a delayed Restate message.
func (d *ECRRepositoryDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[ECRRepositoryState](ctx, drivers.StateKey)
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
	if state.Outputs.RepositoryName == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) { return time.Now().UTC().Format(time.RFC3339), nil })
	if err != nil {
		return types.ReconcileResult{}, err
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.DescribeRepository(rc, state.Outputs.RepositoryName)
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("ECR repository %s was deleted externally", state.Outputs.RepositoryName)
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
	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}
	if drift && state.Mode == types.ModeManaged {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		if err := d.applyMutableUpdates(ctx, api, state.Desired, observed); err != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: err.Error()}, nil
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}
	if drift && state.Mode == types.ModeObserved {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

// GetStatus is a shared (read-only) handler that returns the current lifecycle status.
func (d *ECRRepositoryDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[ECRRepositoryState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a shared (read-only) handler that returns the provisioned resource outputs.
func (d *ECRRepositoryDriver) GetOutputs(ctx restate.ObjectSharedContext) (ECRRepositoryOutputs, error) {
	state, err := restate.Get[ECRRepositoryState](ctx, drivers.StateKey)
	if err != nil {
		return ECRRepositoryOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *ECRRepositoryDriver) GetInputs(ctx restate.ObjectSharedContext) (ECRRepositorySpec, error) {
	state, err := restate.Get[ECRRepositoryState](ctx, drivers.StateKey)
	if err != nil {
		return ECRRepositorySpec{}, err
	}
	return state.Desired, nil
}

func (d *ECRRepositoryDriver) describeExisting(ctx restate.ObjectContext, api RepositoryAPI, name string) (ObservedState, bool, error) {
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeRepository(rc, name)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, nil
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return ObservedState{}, false, err
	}
	return observed, observed.RepositoryArn != "", nil
}

func (d *ECRRepositoryDriver) applyMutableUpdates(ctx restate.ObjectContext, api RepositoryAPI, spec ECRRepositorySpec, observed ObservedState) error {
	if spec.ImageTagMutability != observed.ImageTagMutability {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateImageTagMutability(rc, spec.RepositoryName, spec.ImageTagMutability)
		})
		if err != nil {
			return err
		}
	}
	if !scanningEqual(spec.ImageScanningConfiguration, observed.ImageScanningConfiguration) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateScanningConfiguration(rc, spec.RepositoryName, spec.ImageScanningConfiguration)
		})
		if err != nil {
			return err
		}
	}
	if normalizeJSON(spec.RepositoryPolicy) != normalizeJSON(observed.RepositoryPolicy) {
		if strings.TrimSpace(spec.RepositoryPolicy) == "" {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				deleteErr := api.DeleteRepositoryPolicy(rc, spec.RepositoryName)
				if deleteErr != nil && !IsRepositoryPolicyNotFound(deleteErr) {
					return restate.Void{}, deleteErr
				}
				return restate.Void{}, nil
			})
			if err != nil {
				return err
			}
		} else {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.PutRepositoryPolicy(rc, spec.RepositoryName, spec.RepositoryPolicy)
			})
			if err != nil {
				return err
			}
		}
	}
	if !tagsEqual(spec.Tags, observed.Tags) && observed.RepositoryArn != "" {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.RepositoryArn, tagsForApply(spec.Tags, spec.ManagedKey))
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *ECRRepositoryDriver) scheduleReconcile(ctx restate.ObjectContext, state *ECRRepositoryState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *ECRRepositoryDriver) apiForAccount(ctx restate.ObjectContext, account string) (RepositoryAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("ECRRepositoryDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve ECR repository account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func applyDefaults(spec ECRRepositorySpec) ECRRepositorySpec {
	if spec.ImageTagMutability == "" {
		spec.ImageTagMutability = "MUTABLE"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateProvisionSpec(spec ECRRepositorySpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.RepositoryName) == "" {
		return fmt.Errorf("repositoryName is required")
	}
	if spec.EncryptionConfiguration != nil && spec.EncryptionConfiguration.EncryptionType == "KMS" && strings.TrimSpace(spec.EncryptionConfiguration.KmsKey) == "" {
		return fmt.Errorf("encryptionConfiguration.kmsKey is required when encryptionType is KMS")
	}
	return nil
}
