# Generic Drivers

All 51 production resource services use one lifecycle implementation in
`internal/drivers/kernel`. Resource packages supply typed AWS operations and a
descriptor; they do not implement their own Restate lifecycle handlers or
durable state structs.

## Ownership boundary

The kernel owns:

- the atomic durable state envelope and exact `alpha` version gate;
- Provision, Import, Delete, Reconcile, GetStatus, GetOutputs, GetInputs, and
  ClearState;
- statuses, conditions, generations, errors, tombstones, and scheduling;
- observed-mode and per-resource reconciliation-policy write blocking;
- observe-before-create recovery after ambiguous provider responses;
- optional readiness and late-initialization capability dispatch.

Each resource package owns:

- account and region resolution;
- AWS calls, waiters, and provider-specific idempotency;
- error classification inside journaled `restate.Run` callbacks;
- validation, desired/observed/output mapping, and drift comparison;
- composite-resource sequencing and provider-specific capabilities.

Core owns the adapter between templates/orchestration and each driver. Every
adapter uses `provider.GenericAdapter`; resource-specific behavior is supplied
through typed descriptor hooks rather than copied dispatch, planning, or output
normalization code.

## One production shape

A production resource is complete only when all layers agree:

1. Its CUE schema declares `praxis.io/alpha`, structured metadata, spec, and
   outputs.
2. Its package supplies a typed kernel descriptor and both shared lifecycle
   suites.
3. Its domain pack binds the driver through `genericbinding.Reflect`.
4. Core registers a typed `GenericAdapter` descriptor.
5. Integration inventory contains one driver test file for the resource.

`internal/driverpack/conformance_test.go` and
`internal/driverpack/cross_layer_conformance_test.go` enforce these invariants.

## Alpha version rule

Praxis supports exactly one contract version: `alpha`.

- Templates use `apiVersion: "praxis.io/alpha"`.
- Generic state and saved plans use `version: "alpha"`.
- Missing, different, or unknown versions are rejected.
- There are no migrations, dual reads, aliases, or compatibility shims.

Version fields remain explicit so the one alpha contract can change
deliberately. Breaking existing alpha templates, plans, or state is acceptable.
Backward compatibility requires explicit owner approval.

## Error and reconciliation rules

- Every AWS call is journaled and classified inside its `restate.Run` callback,
  normally through `drivers.RunAWS`.
- Terminal provider errors stay terminal; transient errors remain bare so
  Restate owns retries.
- Provision is idempotent create-or-converge.
- Reconcile always reports drift. The resource's reconciliation policy decides
  whether Praxis also writes to correct it.
- Ignored fields are excluded from both planning and periodic correction.
- Externally deleted resources report replacement required; Core owns replaying
  the deployment graph according to the selected recovery policy.

See [DRIVERS.md](DRIVERS.md) for handler semantics and
[ERRORS.md](ERRORS.md) for the error contract.
