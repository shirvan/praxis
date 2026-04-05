package ami

import (
	"context"
	"fmt"
	"maps"
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

// AMIDriver implements the Praxis driver interface for Amazon Machine Images.
// Uses Restate Virtual Object semantics with exclusive per-key access.
type AMIDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) AMIAPI
}

// NewAMIDriver creates a production AMI driver using the default EC2 client factory.
func NewAMIDriver(auth authservice.AuthClient) *AMIDriver {
	return NewAMIDriverWithFactory(auth, func(cfg aws.Config) AMIAPI {
		return NewAMIAPI(awsclient.NewEC2Client(cfg))
	})
}

// NewAMIDriverWithFactory creates an AMI driver with a custom AMIAPI factory (for testing).
func NewAMIDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) AMIAPI) *AMIDriver {
	if factory == nil {
		factory = func(cfg aws.Config) AMIAPI {
			return NewAMIAPI(awsclient.NewEC2Client(cfg))
		}
	}
	return &AMIDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object name.
func (d *AMIDriver) ServiceName() string {
	return ServiceName
}

// Provision implements the idempotent create-or-converge pattern for AMIs.
//
// Flow:
//  1. Validate required fields (region, name, source). Apply defaults for managedKey and name.
//  2. Load state, increment generation, set status=Provisioning.
//  3. If an image ID exists in state, verify it still exists in AWS.
//  4. If no image but managedKey is set, search by tag (FindByManagedKey) for recovery.
//  5. If no image exists: create via RegisterImage or CopyImage, apply tags, wait until
//     available, then apply mutable attributes (description, permissions, deprecation).
//  6. If image exists: converge mutable attributes in-place.
//  7. Final DescribeImage to capture outputs. Set status=Ready, schedule reconcile.
//
// All AWS calls are wrapped in restate.Run() for durable journaling.
func (d *AMIDriver) Provision(ctx restate.ObjectContext, spec AMISpec) (AMIOutputs, error) {
	if spec.ManagedKey == "" {
		spec.ManagedKey = restate.Key(ctx)
	}
	if spec.Name == "" {
		spec.Name = strings.TrimSpace(spec.Tags["Name"])
	}
	ctx.Log().Info("provisioning AMI", "name", spec.Name, "key", restate.Key(ctx))

	api, _, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return AMIOutputs{}, restate.TerminalError(err, 400)
	}
	if spec.Region == "" {
		return AMIOutputs{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
	}
	if spec.Name == "" {
		return AMIOutputs{}, restate.TerminalError(fmt.Errorf("name is required"), 400)
	}
	if err := validateSource(spec.Source); err != nil {
		return AMIOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[AMIState](ctx, drivers.StateKey)
	if err != nil {
		return AMIOutputs{}, err
	}

	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	imageID := state.Outputs.ImageId
	if imageID != "" {
		_, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, err := api.DescribeImage(rc, imageID)
			if err != nil {
				if IsNotFound(err) {
					return ObservedState{}, restate.TerminalError(err, 404)
				}
				return ObservedState{}, err
			}
			return obs, nil
		})
		if descErr != nil {
			imageID = ""
		}
	}

	if imageID == "" && spec.ManagedKey != "" {
		foundID, findErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, err := api.FindByManagedKey(rc, spec.ManagedKey)
			if err != nil {
				if strings.Contains(err.Error(), "ownership corruption") {
					return "", restate.TerminalError(err, 500)
				}
				return "", err
			}
			return id, nil
		})
		if findErr != nil {
			state.Status = types.StatusError
			state.Error = findErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return AMIOutputs{}, findErr
		}
		if foundID != "" {
			imageID = foundID
		}
	}

	if imageID == "" {
		imageID, err = d.createAMI(ctx, api, spec, &state)
		if err != nil {
			return AMIOutputs{}, err
		}
	} else {
		if err := d.applyMutableAttributes(ctx, api, imageID, spec, state.Observed, &state); err != nil {
			return AMIOutputs{}, err
		}
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, err := api.DescribeImage(rc, imageID)
		if err != nil {
			if IsNotFound(err) {
				return ObservedState{}, restate.TerminalError(err, 404)
			}
			return ObservedState{}, err
		}
		return obs, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = AMIOutputs{ImageId: imageID}
		restate.Set(ctx, drivers.StateKey, state)
		return AMIOutputs{}, err
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

// createAMI handles first-time AMI creation (either RegisterImage or CopyImage),
// followed by tag application, availability wait, and mutable attribute setup.
func (d *AMIDriver) createAMI(ctx restate.ObjectContext, api AMIAPI, spec AMISpec, state *AMIState) (string, error) {
	imageID, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		if spec.Source.FromSnapshot != nil {
			id, err := api.RegisterImage(rc, spec)
			if err != nil {
				if IsSnapshotNotFound(err) || IsInvalidParam(err) {
					return "", restate.TerminalError(err, 400)
				}
				if IsAMIQuotaExceeded(err) {
					return "", restate.TerminalError(err, 503)
				}
				return "", err
			}
			return id, nil
		}
		id, err := api.CopyImage(rc, spec)
		if err != nil {
			if IsInvalidParam(err) {
				return "", restate.TerminalError(err, 400)
			}
			if IsAMIQuotaExceeded(err) {
				return "", restate.TerminalError(err, 503)
			}
			return "", err
		}
		return id, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, *state)
		return "", err
	}

	if err := d.updateTags(ctx, api, imageID, spec, state); err != nil {
		return "", err
	}
	if err := d.waitUntilAvailable(ctx, api, imageID, state); err != nil {
		return "", err
	}
	if err := d.applyMutableAttributes(ctx, api, imageID, spec, ObservedState{}, state); err != nil {
		return "", err
	}
	return imageID, nil
}

// Import adopts an existing AMI into Praxis management.
// Supports both AMI ID and AMI name as resource identifiers.
// Stamps the praxis:managed-key tag on the AMI if not already present.
func (d *AMIDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (AMIOutputs, error) {
	ctx.Log().Info("importing AMI", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return AMIOutputs{}, restate.TerminalError(err, 400)
	}

	mode := defaultAMIImportMode(ref.Mode)
	state, err := restate.Get[AMIState](ctx, drivers.StateKey)
	if err != nil {
		return AMIOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return resolveImportImage(rc, api, ref.ResourceID)
	})
	if err != nil {
		if IsNotFound(err) {
			return AMIOutputs{}, restate.TerminalError(fmt.Errorf("import failed: AMI %s does not exist", ref.ResourceID), 404)
		}
		return AMIOutputs{}, err
	}

	if observed.Tags["praxis:managed-key"] != restate.Key(ctx) {
		allTags := mergeTags(drivers.FilterPraxisTags(observed.Tags), map[string]string{
			"Name":               observed.Name,
			"praxis:managed-key": restate.Key(ctx),
		})
		_, tagErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if err := api.UpdateTags(rc, observed.ImageId, allTags); err != nil {
				return restate.Void{}, err
			}
			return restate.Void{}, nil
		})
		if tagErr != nil {
			return AMIOutputs{}, tagErr
		}
		observed.Tags = allTags
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
	state.Mode = mode
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// resolveImportImage attempts to find the AMI, first by ID (if it looks like ami-xxx),
// then by name. This allows users to import by either identifier.
func resolveImportImage(ctx context.Context, api AMIAPI, resourceID string) (ObservedState, error) {
	resourceID = strings.TrimSpace(resourceID)
	if looksLikeAMIID(resourceID) {
		obs, err := api.DescribeImage(ctx, resourceID)
		if err == nil {
			return obs, nil
		}
		if !IsNotFound(err) {
			return ObservedState{}, err
		}
	}
	return api.DescribeImageByName(ctx, resourceID)
}

// Delete deregisters the AMI. Observed-mode resources cannot be deleted (terminal 409).
// DeregisterImage is idempotent — NotFound is suppressed.
func (d *AMIDriver) Delete(ctx restate.ObjectContext) error {
	state, err := restate.Get[AMIState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete AMI %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.ImageId), 409)
	}

	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	if state.Outputs.ImageId == "" {
		restate.Set(ctx, drivers.StateKey, AMIState{Status: types.StatusDeleted})
		return nil
	}

	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if err := api.DeregisterImage(rc, state.Outputs.ImageId); err != nil {
			if IsNotFound(err) {
				return restate.Void{}, nil
			}
			return restate.Void{}, err
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}

	restate.Set(ctx, drivers.StateKey, AMIState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-detection and correction loop for AMIs.
// Detects external deregistration, failed state, and mutable attribute drift.
// In Managed mode, drift is corrected. In Observed mode, drift is reported only.
func (d *AMIDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[AMIState](ctx, drivers.StateKey)
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
	if state.Outputs.ImageId == "" {
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
		obs, err := api.DescribeImage(rc, state.Outputs.ImageId)
		if err != nil {
			if IsNotFound(err) {
				return ObservedState{}, restate.TerminalError(err, 404)
			}
			return ObservedState{}, err
		}
		return obs, nil
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("AMI %s was deregistered externally", state.Outputs.ImageId)
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
	if observed.State == "failed" {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("AMI %s is in failed state", observed.ImageId)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: state.Error}, nil
	}

	drift := HasDrift(state.Desired, observed)
	if drift && state.Mode == types.ModeManaged {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		if err := d.correctDrift(ctx, api, observed.ImageId, state.Desired, observed); err != nil {
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

	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

// correctDrift applies in-place updates to bring the AMI back to the desired spec.
// Corrects: description, tags, launch permissions, and deprecation schedule.
func (d *AMIDriver) correctDrift(ctx restate.ObjectContext, api AMIAPI, imageID string, desired AMISpec, observed ObservedState) error {
	if desired.Description != observed.Description {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyDescription(rc, imageID, desired.Description)
		})
		if err != nil {
			return fmt.Errorf("modify description: %w", err)
		}
	}

	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		allTags := desiredTags(desired)
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, imageID, allTags)
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}

	if hasLaunchPermDrift(desired.LaunchPermissions, observed) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyLaunchPermissions(rc, imageID, desired.LaunchPermissions)
		})
		if err != nil {
			return fmt.Errorf("modify launch permissions: %w", err)
		}
	}

	if hasDeprecationDrift(desired.Deprecation, observed.DeprecationTime) {
		if desired.Deprecation == nil {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.DisableDeprecation(rc, imageID)
			})
			if err != nil {
				return fmt.Errorf("disable deprecation: %w", err)
			}
		} else {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.EnableDeprecation(rc, imageID, desired.Deprecation.DeprecateAt)
			})
			if err != nil {
				return fmt.Errorf("enable deprecation: %w", err)
			}
		}
	}

	return nil
}

// GetStatus returns the current lifecycle status (shared/concurrent handler).
func (d *AMIDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[AMIState](ctx, drivers.StateKey)
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
func (d *AMIDriver) GetOutputs(ctx restate.ObjectSharedContext) (AMIOutputs, error) {
	state, err := restate.Get[AMIState](ctx, drivers.StateKey)
	if err != nil {
		return AMIOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *AMIDriver) GetInputs(ctx restate.ObjectSharedContext) (AMISpec, error) {
	state, err := restate.Get[AMIState](ctx, drivers.StateKey)
	if err != nil {
		return AMISpec{}, err
	}
	return state.Desired, nil
}

// updateTags applies the full desired tag set (user + praxis:managed-key) to the AMI.
func (d *AMIDriver) updateTags(ctx restate.ObjectContext, api AMIAPI, imageID string, spec AMISpec, state *AMIState) error {
	allTags := desiredTags(spec)
	_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if err := api.UpdateTags(rc, imageID, allTags); err != nil {
			return restate.Void{}, err
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = AMIOutputs{ImageId: imageID}
		restate.Set(ctx, drivers.StateKey, *state)
		return err
	}
	return nil
}

// waitUntilAvailable blocks until the AMI reaches "available" state (up to 10 min).
func (d *AMIDriver) waitUntilAvailable(ctx restate.ObjectContext, api AMIAPI, imageID string, state *AMIState) error {
	_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if err := api.WaitUntilAvailable(rc, imageID, 10*time.Minute); err != nil {
			return restate.Void{}, err
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("AMI %s created but failed to reach available state: %v", imageID, err)
		state.Outputs = AMIOutputs{ImageId: imageID}
		restate.Set(ctx, drivers.StateKey, *state)
		return err
	}
	return nil
}

// applyMutableAttributes converges description, tags, launch permissions, and deprecation.
func (d *AMIDriver) applyMutableAttributes(ctx restate.ObjectContext, api AMIAPI, imageID string, spec AMISpec, observed ObservedState, state *AMIState) error {
	if desiredDescription := spec.Description; desiredDescription != observed.Description {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyDescription(rc, imageID, desiredDescription)
		})
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			state.Outputs = AMIOutputs{ImageId: imageID}
			restate.Set(ctx, drivers.StateKey, *state)
			return restate.TerminalError(err, 500)
		}
	}

	if !drivers.TagsMatch(spec.Tags, observed.Tags) {
		if err := d.updateTags(ctx, api, imageID, spec, state); err != nil {
			return restate.TerminalError(err, 500)
		}
	}

	if hasLaunchPermDrift(spec.LaunchPermissions, observed) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyLaunchPermissions(rc, imageID, spec.LaunchPermissions)
		})
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			state.Outputs = AMIOutputs{ImageId: imageID}
			restate.Set(ctx, drivers.StateKey, *state)
			return restate.TerminalError(err, 500)
		}
	}

	if hasDeprecationDrift(spec.Deprecation, observed.DeprecationTime) {
		var err error
		if spec.Deprecation == nil {
			_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.DisableDeprecation(rc, imageID)
			})
		} else {
			_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.EnableDeprecation(rc, imageID, spec.Deprecation.DeprecateAt)
			})
		}
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			state.Outputs = AMIOutputs{ImageId: imageID}
			restate.Set(ctx, drivers.StateKey, *state)
			return restate.TerminalError(err, 500)
		}
	}

	return nil
}

// scheduleReconcile enqueues a delayed Reconcile message. Dedup guard prevents stacking.
func (d *AMIDriver) scheduleReconcile(ctx restate.ObjectContext, state *AMIState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

// apiForAccount resolves AWS credentials and creates an AMIAPI for the given Praxis account.
func (d *AMIDriver) apiForAccount(ctx restate.ObjectContext, account string) (AMIAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("AMIDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve AMI account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

// validateSource ensures exactly one of FromSnapshot or FromAMI is set, with required sub-fields.
func validateSource(source SourceSpec) error {
	hasSnapshot := source.FromSnapshot != nil
	hasAMI := source.FromAMI != nil
	if !hasSnapshot && !hasAMI {
		return fmt.Errorf("exactly one of source.fromSnapshot or source.fromAMI must be specified")
	}
	if hasSnapshot && hasAMI {
		return fmt.Errorf("cannot specify both source.fromSnapshot and source.fromAMI")
	}
	if hasSnapshot {
		if strings.TrimSpace(source.FromSnapshot.SnapshotId) == "" {
			return fmt.Errorf("source.fromSnapshot.snapshotId is required")
		}
	}
	if hasAMI {
		if strings.TrimSpace(source.FromAMI.SourceImageId) == "" {
			return fmt.Errorf("source.fromAMI.sourceImageId is required")
		}
	}
	return nil
}

// desiredTags merges user tags with the Name and praxis:managed-key system tags.
func desiredTags(spec AMISpec) map[string]string {
	return mergeTags(spec.Tags, map[string]string{
		"Name":               spec.Name,
		"praxis:managed-key": spec.ManagedKey,
	})
}

// mergeTags combines base and extras maps. Empty-value extras are skipped.
func mergeTags(base, extras map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extras))
	maps.Copy(out, base)
	for key, value := range extras {
		if strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	return out
}

// outputsFromObserved maps ObservedState fields to user-facing AMIOutputs.
func outputsFromObserved(observed ObservedState) AMIOutputs {
	return AMIOutputs{
		ImageId:            observed.ImageId,
		Name:               observed.Name,
		State:              observed.State,
		Architecture:       observed.Architecture,
		VirtualizationType: observed.VirtualizationType,
		RootDeviceName:     observed.RootDeviceName,
		OwnerId:            observed.OwnerId,
		CreationDate:       observed.CreationDate,
	}
}

// specFromObserved reconstructs an AMISpec from observed AWS state for Import.
func specFromObserved(observed ObservedState) AMISpec {
	spec := AMISpec{
		Name:        observed.Name,
		Description: observed.Description,
		Source: SourceSpec{
			FromAMI: &FromAMISpec{SourceImageId: observed.ImageId},
		},
		Tags: drivers.FilterPraxisTags(observed.Tags),
	}
	if perms := launchPermsFromObserved(observed); perms != nil {
		spec.LaunchPermissions = perms
	}
	if observed.DeprecationTime != "" {
		spec.Deprecation = &DeprecationSpec{DeprecateAt: observed.DeprecationTime}
	}
	return spec
}

// defaultAMIImportMode returns Observed if no mode was explicitly specified.
func defaultAMIImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

// looksLikeAMIID returns true if the value starts with "ami-" (case-insensitive).
func looksLikeAMIID(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.HasPrefix(value, "ami-")
}
