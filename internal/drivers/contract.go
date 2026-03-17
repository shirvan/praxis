// Package driver defines shared types and constants that all driver services use.
// It lives separate from pkg/types because it contains internal implementation
// details that external driver authors should not depend on.
//
// # Driver Service Contract
//
// Every driver service is a Restate Virtual Object with these handlers:
//
// EXCLUSIVE HANDLERS (ObjectContext — single-writer, read-write state):
//
//	Provision(ctx restate.ObjectContext, spec T) (OutputsT, error)
//	Import(ctx restate.ObjectContext, ref types.ImportRef) (OutputsT, error)
//	Delete(ctx restate.ObjectContext) error
//	Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error)
//
// SHARED HANDLERS (ObjectSharedContext — concurrent reads, read-only state):
//
//	GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error)
//	GetOutputs(ctx restate.ObjectSharedContext) (OutputsT, error)
//
// The distinction between exclusive and shared handlers is critical:
//   - Exclusive handlers (ObjectContext) run one-at-a-time per object key,
//     which prevents racing two updates to the same resource.
//   - Shared handlers (ObjectSharedContext) can run concurrently and only
//     read state, so GetStatus/GetOutputs never block Provision or Reconcile.
//
// Spec type T and Outputs type OutputsT are driver-specific (e.g., S3BucketSpec
// and S3BucketOutputs). They must be JSON-serializable since Restate uses
// encoding/json by default for handler input/output serialization.
//
// The Restate SDK uses reflection (restate.Reflect) to discover handlers from
// struct methods — there is no Go interface to implement.
package drivers
