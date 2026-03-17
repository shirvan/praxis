package s3

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/internal/drivers"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

// ServiceName is the Restate Virtual Object name for S3 buckets.
// This is the user-facing API surface (e.g., curl .../S3Bucket/key/Provision).
// It is a v1.0.0 API surface — changing it later breaks all ingress URLs and clients.
const ServiceName = "S3Bucket"

// S3BucketDriver is a Restate Virtual Object that manages S3 bucket lifecycle.
// Each instance is keyed by a stable resource identifier (e.g. "my-bucket").
//
// Restate guarantees via the Virtual Object model:
//   - Single-writer: only one exclusive handler runs per key at a time
//     (no racing two Provision calls for the same bucket)
//   - Built-in K/V state: all driver state stored atomically per-key
//   - Durable execution: if the service crashes mid-Provision, Restate replays
//     from the journal — completed restate.Run() calls are not re-executed
type S3BucketDriver struct {
	auth       *auth.Registry
	apiFactory func(aws.Config) S3API
}

// NewS3BucketDriver creates a new S3BucketDriver that resolves AWS clients per request.
func NewS3BucketDriver(accounts *auth.Registry) *S3BucketDriver {
	return NewS3BucketDriverWithFactory(accounts, func(cfg aws.Config) S3API {
		return NewS3API(awsclient.NewS3Client(cfg))
	})
}

func NewS3BucketDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) S3API) *S3BucketDriver {
	if accounts == nil {
		accounts = auth.LoadFromEnv()
	}
	if factory == nil {
		factory = func(cfg aws.Config) S3API {
			return NewS3API(awsclient.NewS3Client(cfg))
		}
	}
	return &S3BucketDriver{auth: accounts, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object name.
// The Restate SDK uses this method to register the service under the correct name.
func (d *S3BucketDriver) ServiceName() string {
	return ServiceName
}

// Provision implements "ensure desired state" semantics — it is idempotent by design:
//  1. If the bucket does not exist, create it and apply configuration.
//  2. If it already exists and matches the desired spec, succeed and return stored outputs.
//  3. If it already exists but differs from the desired spec, converge it.
//  4. If it already exists but is owned by another account, fail terminally.
//
// This makes Provision naturally convergent and aligns with how operators expect
// declarative infrastructure systems to behave.
func (d *S3BucketDriver) Provision(ctx restate.ObjectContext, spec S3BucketSpec) (S3BucketOutputs, error) {
	ctx.Log().Info("provisioning bucket", "bucket", spec.BucketName, "key", restate.Key(ctx))
	api, err := d.apiForAccount(spec.Account)
	if err != nil {
		return S3BucketOutputs{}, restate.TerminalError(err, 400)
	}

	// --- Input validation ---
	// Drivers validate required fields regardless of upstream schema validation.
	if spec.BucketName == "" {
		return S3BucketOutputs{}, restate.TerminalError(
			fmt.Errorf("bucketName is required"), 400,
		)
	}
	if spec.Region == "" {
		return S3BucketOutputs{}, restate.TerminalError(
			fmt.Errorf("region is required"), 400,
		)
	}

	// --- Load current state (single atomic read) ---
	state, err := restate.Get[S3BucketState](ctx, drivers.StateKey)
	if err != nil {
		return S3BucketOutputs{}, err
	}

	// --- Store desired state and bump generation ---
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	// --- Check if bucket already exists (idempotent create) ---
	bucketExists := false
	existsResult, err := restate.Run(ctx, func(rc restate.RunContext) (bool, error) {
		if headErr := api.HeadBucket(rc, spec.BucketName); headErr == nil {
			return true, nil
		} else if IsNotFound(headErr) {
			return false, nil
		} else {
			return false, headErr
		}
	})
	if err != nil {
		// HeadBucket failed with a non-404 error.
		if IsConflict(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("bucket %s exists but is not controllable by Praxis", spec.BucketName)
			restate.Set(ctx, drivers.StateKey, state)
			return S3BucketOutputs{}, restate.TerminalError(
				fmt.Errorf("%s", state.Error), 409,
			)
		}
		// Transient error — let Restate retry
		return S3BucketOutputs{}, err
	}
	bucketExists = existsResult

	// --- Create bucket if it doesn't exist ---
	if !bucketExists {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if err := api.CreateBucket(rc, spec.BucketName, spec.Region); err != nil {
				if IsConflict(err) {
					return restate.Void{}, restate.TerminalError(err, 409)
				}
				return restate.Void{}, err
			}
			return restate.Void{}, nil
		})
		if err != nil {
			if IsConflict(err) {
				// BucketAlreadyOwnedByYou — bucket was created between HeadBucket and CreateBucket.
				// This is fine, proceed to configure.
			} else {
				state.Status = types.StatusError
				state.Error = err.Error()
				restate.Set(ctx, drivers.StateKey, state)
				return S3BucketOutputs{}, restate.TerminalError(
					fmt.Errorf("failed to create bucket %s: %w", spec.BucketName, err), 500,
				)
			}
		}
	}

	// --- Configure the bucket (versioning, encryption, tags) ---
	// This runs on both create and update paths, making Provision convergent.
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.ConfigureBucket(rc, spec)
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return S3BucketOutputs{}, restate.TerminalError(
			fmt.Errorf("failed to configure bucket %s: %w", spec.BucketName, err), 500,
		)
	}

	// --- Build outputs ---
	outputs := S3BucketOutputs{
		ARN:        fmt.Sprintf("arn:aws:s3:::%s", spec.BucketName),
		BucketName: spec.BucketName,
		Region:     spec.Region,
		DomainName: fmt.Sprintf("%s.s3.%s.amazonaws.com", spec.BucketName, spec.Region),
	}

	// --- Commit state atomically ---
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	// --- Schedule reconciliation ---
	d.scheduleReconcile(ctx, &state)

	return outputs, nil
}

// Import captures the current provider state as both the initial desired baseline
// and the initial observed state. This means the first reconciliation after import
// sees no drift — the desired spec matches reality.
//
// A user can later call Provision on an imported resource to update its desired
// spec and diverge from the import baseline.
func (d *S3BucketDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (S3BucketOutputs, error) {
	ctx.Log().Info("importing bucket", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, err := d.apiForAccount(ref.Account)
	if err != nil {
		return S3BucketOutputs{}, restate.TerminalError(err, 400)
	}

	mode := drivers.DefaultMode(ref.Mode)

	// --- Load current state and bump generation ---
	state, err := restate.Get[S3BucketState](ctx, drivers.StateKey)
	if err != nil {
		return S3BucketOutputs{}, err
	}
	state.Generation++

	// --- Describe the existing bucket ---
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.DescribeBucket(rc, ref.ResourceID)
	})
	if err != nil {
		if IsNotFound(err) {
			return S3BucketOutputs{}, restate.TerminalError(
				fmt.Errorf("import failed: bucket %s does not exist", ref.ResourceID), 404,
			)
		}
		// Transient error — let Restate retry
		return S3BucketOutputs{}, err
	}

	// --- Synthesize spec from observed (so first reconcile sees no drift) ---
	spec := specFromObserved(ref.ResourceID, observed)
	spec.Account = ref.Account
	outputs := S3BucketOutputs{
		ARN:        fmt.Sprintf("arn:aws:s3:::%s", ref.ResourceID),
		BucketName: ref.ResourceID,
		Region:     observed.Region,
		DomainName: fmt.Sprintf("%s.s3.%s.amazonaws.com", ref.ResourceID, observed.Region),
	}

	// --- Commit state atomically ---
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

// Delete removes the S3 bucket. Fails terminally if the bucket is non-empty.
// Praxis does not auto-empty buckets — automatically emptying a bucket is
// destructive and can hide data-loss events behind routine infrastructure operations.
//
// After successful deletion, the driver sets status to Deleted as a tombstone.
// Delete never schedules a reconciliation — this is an explicit invariant.
func (d *S3BucketDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting bucket", "key", restate.Key(ctx))

	state, err := restate.Get[S3BucketState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	api, err := d.apiForAccount(state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if err := api.DeleteBucket(rc, state.Desired.BucketName); err != nil {
			// Classify terminal errors inside the callback: restate.Run panics
			// on non-terminal errors, so post-callback classification never runs.
			if IsBucketNotEmpty(err) {
				return restate.Void{}, restate.TerminalError(err, 409)
			}
			if IsNotFound(err) {
				return restate.Void{}, nil // already gone
			}
			return restate.Void{}, err // transient — Restate retries
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("bucket %s is not empty — empty it before deleting", state.Desired.BucketName)
		restate.Set(ctx, drivers.StateKey, state)
		return restate.TerminalError(fmt.Errorf("%s", state.Error), 409)
	}

	// Tombstone: keep Deleted status so GetStatus returns a meaningful response.
	// Core can clean up tombstones later.
	restate.Set(ctx, drivers.StateKey, S3BucketState{
		Status: types.StatusDeleted,
	})
	return nil
}

// Reconcile checks actual state against desired state and corrects drift (Managed)
// or reports it (Observed).
//
// Handles two active statuses:
//   - Ready: Full drift detection and optional correction.
//   - Error: Read-only describe to update observed state, no corrective action.
//     Operators must re-trigger Provision explicitly to recover.
//
// All other statuses (Pending, Provisioning, Deleting, Deleted) are no-ops.
//
// If Reconcile discovers the resource was deleted externally (404), it transitions
// to Error status with a clear message — it does NOT re-provision automatically.
func (d *S3BucketDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[S3BucketState](ctx, drivers.StateKey)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	api, err := d.apiForAccount(state.Desired.Account)
	if err != nil {
		return types.ReconcileResult{}, restate.TerminalError(err, 400)
	}

	// Clear the scheduling guard — we're running now.
	state.ReconcileScheduled = false

	// Only reconcile Ready and Error resources.
	if state.Status != types.StatusReady && state.Status != types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}

	// --- Describe current AWS state ---
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, err := api.DescribeBucket(rc, state.Desired.BucketName)
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
			// Resource was deleted externally — transition to Error, do NOT re-provision.
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("bucket %s was deleted externally", state.Desired.BucketName)
			state.LastReconcile = time.Now().UTC().Format(time.RFC3339)
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Error: state.Error}, nil
		}
		// Transient AWS error — schedule retry, report error.
		state.LastReconcile = time.Now().UTC().Format(time.RFC3339)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}

	// --- Update observed state ---
	state.Observed = observed
	state.LastReconcile = time.Now().UTC().Format(time.RFC3339)

	drift := HasDrift(state.Desired, observed)

	// --- Error status: read-only describe, no correction ---
	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}

	// --- Ready + Managed + drift: correct ---
	if drift && state.Mode == types.ModeManaged {
		ctx.Log().Info("drift detected, correcting", "bucket", state.Desired.BucketName)
		_, configErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ConfigureBucket(rc, state.Desired)
		})
		if configErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: configErr.Error()}, nil
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}

	// --- Ready + Observed + drift: report only ---
	if drift && state.Mode == types.ModeObserved {
		ctx.Log().Info("drift detected (observed mode, not correcting)", "bucket", state.Desired.BucketName)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	// --- No drift ---
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *S3BucketDriver) apiForAccount(account string) (S3API, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, fmt.Errorf("S3BucketDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.Resolve(account)
	if err != nil {
		return nil, fmt.Errorf("resolve S3 account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), nil
}

// GetStatus is a SHARED handler — it can run concurrently with exclusive handlers
// and only reads state. Other services and the API can call this without
// blocking Provision or Reconcile.
func (d *S3BucketDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[S3BucketState](ctx, drivers.StateKey)
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

// GetOutputs is a SHARED handler — returns the resource outputs (ARN, domain, etc.).
func (d *S3BucketDriver) GetOutputs(ctx restate.ObjectSharedContext) (S3BucketOutputs, error) {
	state, err := restate.Get[S3BucketState](ctx, drivers.StateKey)
	if err != nil {
		return S3BucketOutputs{}, err
	}
	return state.Outputs, nil
}

// scheduleReconcile sends a delayed self-invocation to trigger Reconcile.
// This is a durable timer — it survives Restate and service restarts.
//
// Deduplication: If state.ReconcileScheduled is already true, this is a no-op.
// This prevents timer fan-out where Provision, Import, and each Reconcile
// all schedule their own successor, leading to unbounded delayed messages.
// At most one pending reconcile exists per object at any time.
//
// We use a delayed one-way message instead of Sleep because:
//  1. The handler completes immediately (releases the exclusive lock)
//  2. Other requests to this object can be processed during the delay
//  3. No long-running invocation ties up a service deployment version
func (d *S3BucketDriver) scheduleReconcile(ctx restate.ObjectContext, state *S3BucketState) {
	if state.ReconcileScheduled {
		return // already have a pending reconcile
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

// specFromObserved creates an S3BucketSpec from observed AWS state.
// This ensures the first reconciliation after import sees no drift.
func specFromObserved(name string, obs ObservedState) S3BucketSpec {
	versioning := obs.VersioningStatus == "Enabled"
	algo := obs.EncryptionAlgo
	if algo == "" {
		algo = "AES256"
	}
	return S3BucketSpec{
		BucketName: name,
		Region:     obs.Region,
		Versioning: versioning,
		Encryption: EncryptionSpec{
			Enabled:   algo != "",
			Algorithm: algo,
		},
		Tags: obs.Tags,
	}
}
