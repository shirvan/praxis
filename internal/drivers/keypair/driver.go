package keypair

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// KeyPairDriver implements the Praxis driver for AWS EC2 Key Pairs.
type KeyPairDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) KeyPairAPI
}

// NewKeyPairDriver creates a production driver with default EC2 client factory.
func NewKeyPairDriver(auth authservice.AuthClient) *KeyPairDriver {
	return NewKeyPairDriverWithFactory(auth, func(cfg aws.Config) KeyPairAPI {
		return NewKeyPairAPI(awsclient.NewEC2Client(cfg))
	})
}

// NewKeyPairDriverWithFactory creates a driver with a custom KeyPairAPI factory (for testing).
func NewKeyPairDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) KeyPairAPI) *KeyPairDriver {
	if factory == nil {
		factory = func(cfg aws.Config) KeyPairAPI {
			return NewKeyPairAPI(awsclient.NewEC2Client(cfg))
		}
	}
	return &KeyPairDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object name used for routing.
func (d *KeyPairDriver) ServiceName() string {
	return ServiceName
}

// Provision creates or converges an EC2 Key Pair.
//
// Flow:
//  1. Validate required fields (region, keyName, keyType). Apply defaults.
//  2. Load state, increment generation, set status=Provisioning.
//  3. If a key pair ID exists in state, verify it still exists in AWS.
//  4. If no key pair exists: CreateKeyPair (AWS-generated) or ImportKeyPair
//     (user-provided public key). Private key is only returned on CreateKeyPair.
//  5. If key pair already exists: converge tags only.
//  6. Final DescribeKeyPair to capture outputs. Set status=Ready, schedule reconcile.
//
// Private key material is returned to the caller but NOT persisted in state.
func (d *KeyPairDriver) Provision(ctx restate.ObjectContext, spec KeyPairSpec) (KeyPairOutputs, error) {
	ctx.Log().Info("provisioning key pair", "key", restate.Key(ctx), "keyName", spec.KeyName)
	api, _, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return KeyPairOutputs{}, restate.TerminalError(err, 400)
	}

	spec = applyDefaults(spec)
	if spec.Region == "" {
		return KeyPairOutputs{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
	}
	if spec.KeyName == "" {
		return KeyPairOutputs{}, restate.TerminalError(fmt.Errorf("keyName is required"), 400)
	}
	if spec.KeyType != "rsa" && spec.KeyType != "ed25519" {
		return KeyPairOutputs{}, restate.TerminalError(fmt.Errorf("keyType must be \"rsa\" or \"ed25519\""), 400)
	}

	state, err := restate.Get[KeyPairState](ctx, drivers.StateKey)
	if err != nil {
		return KeyPairOutputs{}, err
	}

	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	keyPairID := state.Outputs.KeyPairId
	if keyPairID != "" {
		descResult, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribeKeyPair(rc, spec.KeyName)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 404)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr != nil || descResult.KeyPairId == "" {
			keyPairID = ""
		}
	}

	outputs := state.Outputs
	if keyPairID == "" {
		if spec.PublicKeyMaterial != "" {
			result, runErr := restate.Run(ctx, func(rc restate.RunContext) (KeyPairOutputs, error) {
				newKeyPairID, fingerprint, importErr := api.ImportKeyPair(rc, spec.KeyName, spec.PublicKeyMaterial, spec.Tags)
				if importErr != nil {
					if IsDuplicate(importErr) {
						return KeyPairOutputs{}, restate.TerminalError(importErr, 409)
					}
					if IsInvalidKeyFormat(importErr) {
						return KeyPairOutputs{}, restate.TerminalError(importErr, 400)
					}
					return KeyPairOutputs{}, importErr
				}
				return KeyPairOutputs{
					KeyName:        spec.KeyName,
					KeyPairId:      newKeyPairID,
					KeyFingerprint: fingerprint,
					KeyType:        spec.KeyType,
				}, nil
			})
			if runErr != nil {
				state.Status = types.StatusError
				state.Error = runErr.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return KeyPairOutputs{}, runErr
			}
			outputs = result
			keyPairID = result.KeyPairId
		} else {
			result, runErr := restate.Run(ctx, func(rc restate.RunContext) (KeyPairOutputs, error) {
				newKeyPairID, fingerprint, privateKey, createErr := api.CreateKeyPair(rc, spec.KeyName, spec.KeyType, spec.Tags)
				if createErr != nil {
					if IsDuplicate(createErr) {
						return KeyPairOutputs{}, restate.TerminalError(createErr, 409)
					}
					return KeyPairOutputs{}, createErr
				}
				return KeyPairOutputs{
					KeyName:            spec.KeyName,
					KeyPairId:          newKeyPairID,
					KeyFingerprint:     fingerprint,
					KeyType:            spec.KeyType,
					PrivateKeyMaterial: privateKey,
				}, nil
			})
			if runErr != nil {
				state.Status = types.StatusError
				state.Error = runErr.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return KeyPairOutputs{}, runErr
			}
			outputs = result
			keyPairID = result.KeyPairId
		}
	} else if !drivers.TagsMatch(spec.Tags, state.Observed.Tags) {
		_, runErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, keyPairID, spec.Tags)
		})
		if runErr != nil {
			state.Status = types.StatusError
			state.Error = runErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return KeyPairOutputs{}, runErr
		}
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeKeyPair(rc, spec.KeyName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = KeyPairOutputs{KeyName: spec.KeyName, KeyPairId: keyPairID, KeyType: spec.KeyType}
		restate.Set(ctx, drivers.StateKey, state)
		return KeyPairOutputs{}, err
	}

	privateKeyForReturn := outputs.PrivateKeyMaterial
	outputs = outputsFromObserved(observed)

	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)

	outputs.PrivateKeyMaterial = privateKeyForReturn
	return outputs, nil
}

// Import adopts an existing EC2 Key Pair by name into Praxis management.
func (d *KeyPairDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (KeyPairOutputs, error) {
	ctx.Log().Info("importing key pair", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return KeyPairOutputs{}, restate.TerminalError(err, 400)
	}

	mode := drivers.DefaultMode(ref.Mode)
	state, err := restate.Get[KeyPairState](ctx, drivers.StateKey)
	if err != nil {
		return KeyPairOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeKeyPair(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: key pair %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return KeyPairOutputs{}, err
	}

	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region

	outputs := outputsFromObserved(observed)
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Mode = mode
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Delete removes the key pair from AWS. Observed-mode resources cannot be deleted (409).
// DeleteKeyPair is idempotent — NotFound is suppressed.
func (d *KeyPairDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting key pair", "key", restate.Key(ctx))
	state, err := restate.Get[KeyPairState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete key pair in Observed mode; re-import with --mode managed to allow deletion"), 409)
	}

	keyName := state.Desired.KeyName
	if keyName == "" {
		keyName = state.Outputs.KeyName
	}
	if keyName == "" {
		restate.Set(ctx, drivers.StateKey, KeyPairState{Status: types.StatusDeleted})
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
		runErr := api.DeleteKeyPair(rc, keyName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
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

	restate.Set(ctx, drivers.StateKey, KeyPairState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-detection and correction loop.
// Detects external deletion, tag drift, and corrects drift in Managed mode.
func (d *KeyPairDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[KeyPairState](ctx, drivers.StateKey)
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

	keyName := state.Outputs.KeyName
	if keyName == "" {
		keyName = state.Desired.KeyName
	}
	if keyName == "" {
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
		obs, runErr := api.DescribeKeyPair(rc, keyName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("key pair %s was deleted externally", keyName)
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

	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}

	if drift && state.Mode == types.ModeManaged {
		ctx.Log().Info("drift detected, correcting key pair", "keyName", keyName)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		correctionErr := d.correctDrift(ctx, api, observed.KeyPairId, state.Desired, observed)
		if correctionErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		refreshed, refreshErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.DescribeKeyPair(rc, keyName)
		})
		if refreshErr == nil {
			state.Observed = refreshed
			state.Outputs = outputsFromObserved(refreshed)
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}

	if drift && state.Mode == types.ModeObserved {
		ctx.Log().Info("drift detected (observed mode, not correcting)", "keyName", keyName)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	state.Outputs = outputsFromObserved(observed)
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

// GetStatus returns the current lifecycle status (shared/concurrent handler).
func (d *KeyPairDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[KeyPairState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{
		Status:     state.Status,
		Mode:       state.Mode,
		Generation: state.Generation,
		Error:      state.Error,
	}, nil
}

// GetOutputs returns the provisioned outputs (shared/concurrent handler).
func (d *KeyPairDriver) GetOutputs(ctx restate.ObjectSharedContext) (KeyPairOutputs, error) {
	state, err := restate.Get[KeyPairState](ctx, drivers.StateKey)
	if err != nil {
		return KeyPairOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *KeyPairDriver) GetInputs(ctx restate.ObjectSharedContext) (KeyPairSpec, error) {
	state, err := restate.Get[KeyPairState](ctx, drivers.StateKey)
	if err != nil {
		return KeyPairSpec{}, err
	}
	return state.Desired, nil
}

// correctDrift applies in-place tag updates to bring the key pair back to desired state.
func (d *KeyPairDriver) correctDrift(ctx restate.ObjectContext, api KeyPairAPI, keyPairID string, desired KeyPairSpec, observed ObservedState) error {
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, keyPairID, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}
	return nil
}

// scheduleReconcile enqueues a delayed Reconcile message with dedup guard.
func (d *KeyPairDriver) scheduleReconcile(ctx restate.ObjectContext, state *KeyPairState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

// apiForAccount resolves AWS credentials and creates a KeyPairAPI for the given Praxis account.
func (d *KeyPairDriver) apiForAccount(ctx restate.ObjectContext, account string) (KeyPairAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("KeyPairDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve key pair account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

// applyDefaults sets KeyType to "ed25519" if empty and initializes nil Tags.
func applyDefaults(spec KeyPairSpec) KeyPairSpec {
	if spec.KeyType == "" {
		spec.KeyType = "ed25519"
	}
	if spec.Tags == nil {
		spec.Tags = make(map[string]string)
	}
	return spec
}

// specFromObserved reconstructs a KeyPairSpec from observed AWS state for Import.
func specFromObserved(obs ObservedState) KeyPairSpec {
	return KeyPairSpec{
		KeyName: obs.KeyName,
		KeyType: obs.KeyType,
		Tags:    drivers.FilterPraxisTags(obs.Tags),
	}
}

// outputsFromObserved maps ObservedState to user-facing KeyPairOutputs.
func outputsFromObserved(obs ObservedState) KeyPairOutputs {
	return KeyPairOutputs{
		KeyName:        obs.KeyName,
		KeyPairId:      obs.KeyPairId,
		KeyFingerprint: obs.KeyFingerprint,
		KeyType:        obs.KeyType,
	}
}
