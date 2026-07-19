# FABLE_FUTURE.md — Roadmap Implementation Guides

Six roadmap-level capabilities, each decomposed into stages small enough for a
less capable model (or a rushed human) to implement safely. This is the
companion to `FABLE.md`: FABLE tracks audit findings and their fixes; this file
covers the architectural items that came out of those audits plus two
capabilities the audits showed are missing (telemetry, ingress hardening).
`docs/FUTURE.md` holds the product-facing summaries; this file holds the
implementation detail.

All code facts below (struct definitions, signatures, file:line) were verified
against commit `ae7cfeb`. **Re-verify before editing** — if a quoted shape no
longer matches, stop and re-read the surrounding code rather than forcing the
sketch to fit.

## Ground rules (apply to every guide)

- Error classification happens inside `restate.Run()` callbacks: terminal
  errors wrapped with `restate.TerminalError()`, transient returned bare
  (`AGENTS.md`, `docs/ERRORS.md`).
- Nothing non-deterministic outside `restate.Run` in handler code: no
  `time.Now()`, no randomness, no map-iteration-order effects. Deterministic
  derivations (FNV of a stable key, sorted iteration) are fine.
- Changing the shape of PERSISTED state (driver state, `DeploymentState`,
  `AuthState`, event records) is a migration: existing deployments have the old
  JSON in Restate K/V. Only add optional fields with `omitempty`; never rename
  or repurpose existing JSON keys; state your compatibility reasoning in the
  commit message.
- After any change: `just lint && gofmt -l . && go test ./internal/... ./pkg/...
  -p 1` (Docker required), plus the targeted integration tests named in each
  stage (`go test -tags integration ./tests/integration/ -run <Pattern>
  -timeout 15m` — needs `docker compose up -d moto && docker compose up
  --exit-code-from moto-init moto-init`).
- One stage per branch/commit. Do not start stage N+1 until stage N's
  verification passes.
- Small-model suitability is marked per stage: **[SAFE]** = mechanical with the
  guide, **[REVIEW]** = implement, then require human/strong-model review
  before merge, **[STOP]** = do not attempt without a strong model; produce a
  design note instead.

---

## R1 — State-derived rollback: events are observability, state is truth

### Why

`DeploymentEventStore.RollbackPlan` builds the rollback resource set
exclusively by scanning `resource.ready` events. Two structural consequences:

1. Retention pruning (`Prune`, event_store.go:249) deletes whole chunks by
   age/count with no knowledge of rollback: a pruned ready event silently
   removes that resource from every future rollback (FABLE §3.3).
2. `resource.ready` emission must stay error-propagating in the deploy
   workflow (workflow.go:346, :743 carry "load-bearing" comments) solely
   because of this coupling — blocking the fire-and-forget eventing model the
   architecture docs describe.

`DeploymentState` is authoritative, never pruned, and already stores
everything rollback needs.

### Current state (verified)

- `RollbackResource{Sequence int64, Name string, Kind string}` and
  `RollbackPlan{DeploymentKey string, Resources []RollbackResource}` —
  `internal/core/orchestrator/types.go:317-333`. No Key, no Outputs, no
  generation on the type.
- The handler (`event_store.go:202-243`) maps `Name` ← event `Subject()`,
  `Kind` ← the `resourcekind` extension, `Sequence` ← store sequence, and
  sorts by reverse sequence.
- **The only production caller is `rollback_workflow.go:74`**, and the loop
  body (rollback_workflow.go:118-241) reads **only `item.Name`** — Kind, Key,
  and Lifecycle all come from `exec.plan[item.Name]`, which is built from
  `DeploymentState` via `planResourcesFromState(state)`
  (runtime.go:409-435). The event-store plan contributes exactly two things:
  the *set* of names that reached Ready, and a reverse-chronological *order*.
- The delete workflow already derives its resource list from state and orders
  by **reverse topological sort** (`delete_workflow.go:143-148`:
  `planResourcesFromState` → `graphFromPlanResources` →
  `schedule.ReadyForDelete`), which respects dependencies exactly — strictly
  safer than reverse-chronological.
- `ResourceState` (types.go:230-241) carries
  `Status types.DeploymentResourceStatus` per resource;
  `DeploymentState.Outputs` is keyed by resource name.
- `praxis rollback --to <gen>` (`handlers_rollback_to.go`) is a **separate
  mechanism** (plan-snapshot replay) and does not touch RollbackPlan — leave
  it alone.
- Integration coverage of the current handler:
  `tests/integration/core_test.go:1998-2007` asserts the plan contents after a
  partial-failure apply.

### Design decision

Replace the event-scan with a state-derived candidate set, ordered by reverse
topological sort (the delete workflow's proven pattern). Reverse-chronological
ordering was only ever an approximation of "delete dependents before
dependencies"; the DAG gives that exactly. Keep the `RollbackPlan` handler as
a read-only observability endpoint but stop the workflow depending on it.

### Stage 1 — state-derived candidate selection in the rollback workflow [REVIEW]

**Files:** `internal/core/orchestrator/rollback_workflow.go`,
`internal/core/orchestrator/runtime.go` (read-only reference).

1. In `DeploymentRollbackWorkflow.Run`, delete the RPC at
   rollback_workflow.go:74 (`restate.Object[RollbackPlan](ctx,
   DeploymentEventStoreServiceName, req.DeploymentKey,
   "RollbackPlan").Request(...)`).
2. The workflow already computes `exec :=
   newExecutionState(planResourcesFromState(state))` and seeds statuses from
   `state.Resources` (rollback_workflow.go:100-115). Build the candidate set
   from the same state:

```go
	// A resource is rollback-eligible when the apply run brought it to Ready.
	// DeploymentState is authoritative and never pruned, unlike the event
	// store this plan used to be derived from.
	eligible := make(map[string]bool, len(state.Resources))
	for name, resource := range state.Resources {
		if resource == nil {
			continue
		}
		if resource.Status == types.DeploymentResourceReady {
			eligible[name] = true
		}
	}
```

   Check `types.DeploymentResourceStatus` constants first (pkg/types) — if a
   distinct "ready in a prior generation" status exists, decide explicitly
   whether `PriorReady` resources belong in a rollback of the *current* failed
   apply (they should NOT be deleted if they pre-existed this generation;
   check how `ResourceState.PriorReady` is set in `deployment_state.go:99-118`
   and exclude `PriorReady` resources, matching the current behavior where
   only this run's ready events counted... verify: the event store is
   per-deployment and accumulates across generations, so the CURRENT behavior
   actually includes prior-generation resources too. Preserve current behavior
   in stage 1 — include all Ready resources — and leave a TODO referencing the
   PriorReady question for a human decision).
3. Replace the flat `for _, item := range rollbackPlan.Resources` loop's
   *iteration source* with reverse-topo order, mirroring
   `delete_workflow.go:143-148`:

```go
	graph, err := graphFromPlanResources(planResourcesFromState(state))
	if err != nil {
		return DeploymentResult{}, restate.TerminalError(fmt.Errorf("invalid stored deployment graph: %w", err), 500)
	}
```

   then drive the existing per-resource body (skip checks, adapter.Delete,
   markDeleted, events) from the delete-scheduling primitive the delete
   workflow uses (`schedule.ReadyForDelete` — read delete_workflow.go's loop
   before writing this; reuse its shape, filtered to `eligible[name]`).
   IMPORTANT: keep every existing per-resource behavior — the
   already-deleted check, `preventDestroy` lifecycle handling, skip
   propagation, the 5-minute `rollbackResourceTimeout`, and all event
   emissions — identical. Only the candidate set and ordering change.
4. Do NOT change `RollbackResource`/`RollbackPlan` types or the event-store
   handler in this stage.

**Verify:** `go test ./internal/core/orchestrator/ -count=1 -race`; then
`go test -tags integration ./tests/integration/ -run 'Rollback' -timeout 20m`.
The core_test.go:1998 assertion still passes because the handler is untouched.
Add one new integration case: apply a 2-resource template where B depends on A
and both reach Ready, then roll back and assert A is deleted *after* B
(reverse-topo order observable via event sequence).

### Stage 2 — decouple ready-event emission [SAFE, blocked on stage 1]

**Files:** `internal/core/orchestrator/workflow.go` (:346 and :743 areas),
`internal/core/orchestrator/runtime.go` (BestEffort doc comment).

Once no production code path reads ready events for correctness:

1. Convert the two `resource.ready` emission sites from error-propagating back
   to `EmitDeploymentCloudEventBestEffort`, replacing the "load-bearing"
   comments with: "informational since rollback derives from DeploymentState".
2. Update `EmitDeploymentCloudEventBestEffort`'s doc comment (runtime.go): the
   remaining load-bearing class is deployment TERMINAL events only (`praxis
   observe` exits on them) — those keep error propagation.
3. Grep for the string "load-bearing" across `internal/` and update every
   stale mention.

**Verify:** unit + `-run 'TestCore|Rollback'` integration.

### Stage 3 — demote the event-store handler [SAFE]

Add a deprecation note to the `RollbackPlan` handler doc comment ("kept as a
read-only observability endpoint; the rollback workflow derives its plan from
DeploymentState") and update `docs/ORCHESTRATOR.md`'s rollback section plus
FABLE.md §3.3's status. Do not delete the handler — the CLI or tests may query
it, and removal is a separate decision.

**Out of scope:** `RollbackTo` (snapshot replay), retention policy changes,
event-store schema changes.

---

## R2 — Generic driver harness

### Why

~70% of each of the 51 drivers is a verbatim lifecycle skeleton (~20k LOC).
Every audit round found the same failure signature: a fix applied in one
driver, latent in others (Conditions implemented by 1/51; waiters duplicated
4×; the jitter sweep missed two drivers purely because of an import alias).
Every new driver copies ~370 lines that can drift.

### Current state (verified)

- Handler set, identical across drivers (kmskey as reference,
  `internal/drivers/kmskey/driver.go`):

```go
	ServiceName() string
	Provision(ctx restate.ObjectContext, spec KMSKeySpec) (KMSKeyOutputs, error)
	Import(ctx restate.ObjectContext, ref types.ImportRef) (KMSKeyOutputs, error)
	Delete(ctx restate.ObjectContext) error
	Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error)
	GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error)
	GetOutputs(ctx restate.ObjectSharedContext) (KMSKeyOutputs, error)
	GetInputs(ctx restate.ObjectSharedContext) (KMSKeySpec, error)
	ClearState(ctx restate.ObjectContext) error
```

  Some drivers additionally have `PreDelete(ctx restate.ObjectContext) error`
  (s3:272, iamrole:688, ecrrepo:462), invoked via the adapter-side
  `provider.PreDeleter` hook (`internal/core/provider/finalizer.go:6`).
- State struct, structurally identical across drivers (JSON keys `desired`,
  `observed`, `outputs`, `status`, `mode`, `error`, `generation`,
  `lastReconcile`, `reconcileScheduled`), stored under the single
  `drivers.StateKey = "state"`.
- Registration: `restate.Reflect(driver, rp)` in `cmd/*/main.go`; the VO name
  comes from the `ServiceName() string` method (sdk-go `serviceNamer`
  interface, reflect.go:14-15). Methods on an instantiated generic type are
  visible to reflection like any other methods.
- Driver struct shape: `{auth authservice.AuthClient, apiFactory func(aws.Config) XAPI}`
  with `apiForAccount(ctx, account)` resolving `aws.Config` through
  `d.auth.GetCredentials` (kmskey/driver.go:426-435).

### Design

One generic driver in a new package `internal/drivers/harness`:

```go
package harness

// Ops is everything kind-specific. S = spec, O = outputs, Obs = observed state.
type Ops[S, O, Obs any] interface {
	// Validate rejects malformed specs (terminal 400 at the harness layer).
	Validate(spec S) error
	// ApplyDefaults fills server-side defaults before validation/compare.
	ApplyDefaults(spec S) S
	// PrepareSpec injects account/region/managedKey (harness calls it with the
	// resolved region and restate.Key).
	PrepareSpec(spec S, region, managedKey string) S
	// Observe describes live state. Runs inside restate.Run — return AWS
	// errors classified per docs/ERRORS.md.
	Observe(rc restate.RunContext, spec S) (obs Obs, found bool, err error)
	// Create provisions the resource. Runs inside restate.Run.
	Create(rc restate.RunContext, spec S) (Obs, error)
	// ReadyStatus reports whether obs is in its terminal-good state, for the
	// harness's bounded durable wait. Return ("", true) for kinds with no
	// transitional states.
	ReadyStatus(obs Obs) (status string, ready bool)
	// Converge applies mutable-field corrections. ctx (not RunContext) because
	// converge issues multiple journaled steps.
	Converge(ctx restate.ObjectContext, api APIHandle, desired S, observed Obs) error
	// HasDrift compares desired vs observed.
	HasDrift(desired S, observed Obs) bool
	// Outputs projects observed state to outputs.
	Outputs(obs Obs) O
	// SpecFromObserved synthesizes a spec for import.
	SpecFromObserved(obs Obs) S
	// ResourceID extracts the provider-native identifier used for Delete and
	// Reconcile lookups (e.g. outputs name/ARN).
	ResourceID(o O) string
	// Delete removes the resource. Runs inside restate.Run; must be
	// idempotent (NotFound → nil).
	Delete(rc restate.RunContext, id string) error
}

type Driver[S, O, Obs any] struct { /* serviceName, auth, opsFactory, wait config */ }
// Provision / Import / Delete / Reconcile / GetStatus / GetOutputs /
// GetInputs / ClearState implemented ONCE, including: single-state-key
// persistence, Conditions on every ReconcileResult (fixing FABLE §5.1),
// ACTIVE-gated converge in both Provision and Reconcile, the bounded durable
// waiter, ReconcileDelayFor scheduling, and the Error-status semantics.
```

The state type is `harness.State[S, O, Obs]` with EXACTLY the JSON tags listed
above — byte-compatible with existing persisted state.

### Stage 1 — build the harness against ONE driver, prove state compatibility [STOP → strong model designs, then SAFE to replicate]

The harness package itself (generics + Restate handler semantics + replay
safety + Error-status semantics) is the hard 20%. A smaller model should NOT
author it from scratch. The staged path:

1. **[STOP]** A strong model/human writes `internal/drivers/harness` plus the
   kmskey migration (kmskey is new-generation, has full unit + integration
   coverage, no PreDelete, no special cases). The kmskey package shrinks to
   `types.go` + `aws.go` + `drift.go` + an `ops.go` implementing `Ops` + a
   2-line constructor.
2. State-compatibility gate (the migration's make-or-break): a unit test that
   marshals the OLD `KMSKeyState` struct (copy it into the test) and
   unmarshals into `harness.State[KMSKeySpec, KMSKeyOutputs, ObservedState]`,
   asserts every field round-trips, and vice versa. NOTE: `LastReconcile` is
   `string` in kmskey but check every driver before its migration — any
   `time.Time` variant needs a per-driver compatibility shim.
3. Full verification: kmskey unit tests, `go test -tags integration
   ./tests/integration/ -run 'TestKMS' `, and a manual crash-resume spot check
   if the restatetest harness supports it.

### Stage 2 — migrate the new-generation drivers [SAFE, one PR each]

dynamodbtable, ekscluster, ecscluster, secret — in that order (ekscluster and
dynamodbtable exercise the waiter hook; secret exercises a Converge with a
restore step). Each migration is mechanical against the kmskey template:
implement `Ops`, delete the skeleton, keep `aws.go`/`drift.go`/`types.go`
untouched, port the driver's quirks into ops (EKS: `ReadyStatus` returns
("ACTIVE", ...) and Converge is update-serialized; secret:
`ScheduledForDeletion` restore in Converge). Acceptance per driver: unit +
targeted integration tests green, state round-trip test present, `git diff
--stat` shows the driver.go net-negative by several hundred lines.

### Stage 3 — sweep the remaining drivers [SAFE, batched]

Oldest last (s3 has PreDelete + LateInit + conditions already — port its
extras as optional harness hooks: `PreDeleteOps`, `LateInitOps` interfaces
asserted dynamically). Batch by pack (network, then storage, ...) so each
release-preflight set stays meaningful. STOP if any driver's state shape
diverges from the standard JSON keys — flag it instead of forcing.

**Out of scope:** changing the Adapter layer, the wire contract, or any CUE
schema. The harness must be invisible from outside the driver process.

---

## R3 — Dynamic adapter registry (config-driven external kinds)

### Why

`docs/EXTENDING.md` promises a dynamic adapter registry "tracked in the
roadmap" (line ~590), and the audit established the current reality: templates
and deployments hard-require a compiled-in adapter
(`pipeline.go` → `providers.Get` → "unsupported resource kind"). Extending
Praxis today means a Go PR. A config-driven passthrough adapter makes the
documented extension story true.

### Current state (verified)

- `Adapter` interface (`internal/core/provider/registry.go:49-99`): `Kind`,
  `ServiceName`, `Scope`, `BuildKey`, `DecodeSpec`, `Provision`, `Delete`,
  `NormalizeOutputs`, `Plan`, `BuildImportKey`, `Import` — plus the
  `ProvisionInvocation`/`DeleteInvocation` handle interfaces
  (registry.go:108-143).
- `NewRegistryWithAdapters(adapters ...Adapter)` (registry.go:224) already
  accepts an arbitrary adapter list — the extension point exists.
- Registry is constructed once in `cmd/praxis-core/main.go:62`
  (`provider.NewRegistry(authClient)`) and injected into the command service
  and all three workflows.
- The wire contract external drivers must speak is already fixed by
  convention: `Provision(spec) → outputs`, `Delete() → void`,
  `GetStatus() → types.StatusResponse`, etc. (EXTENDING.md's handler table),
  with JSON payloads.
- The template engine requires a `#<Kind>` CUE schema (post-audit behavior),
  and the CLI keys `kindScopes` / `sensitiveInputPaths` maps by kind with
  guard tests.

### Design

A `DispatchAdapter` struct implementing `Adapter` from a config record, plus a
loader that reads extension records at praxis-core startup:

```go
// internal/core/provider/dispatch_adapter.go
type ExtensionKind struct {
	Kind        string `json:"kind"`                  // template kind == Restate VO service name
	ServiceName string `json:"serviceName,omitempty"` // defaults to Kind
	Scope       string `json:"scope"`                 // "global" | "region" | "custom"
	// KeyFields: spec paths joined with "~" to form the VO key, e.g.
	// ["spec.region", "metadata.name"]. Mirrors how built-in kinds build keys.
	KeyFields []string `json:"keyFields"`
}
```

Behavioral contract of the passthrough:

- `DecodeSpec` → the raw resource document as `map[string]any` (no typed
  validation beyond the CUE schema, which the operator ships alongside).
- `Provision` → `restate.Object[json.RawMessage](ctx, serviceName, key,
  "Provision").RequestFuture(specJSON)`; `NormalizeOutputs` unmarshals the
  raw JSON into `map[string]any`.
- `Plan` → always `(types.OpCreate, corediff.FieldDiffsFromJSON(spec), nil)`
  with a diff annotation that external kinds have no live-describe — matching
  EXTENDING.md's documented limitation.
- `Import`/`BuildImportKey` → `restate.TerminalError(..., 501)` ("import is
  not supported for external kinds").
- `BuildKey` → resolve `KeyFields` against the resource JSON, validate each
  segment with the existing `ValidateKeyPart`
  (`internal/core/provider/keys.go:57`), join with `~`.

### Stage 1 — DispatchAdapter + unit tests [SAFE]

Implement the struct above in `internal/core/provider/dispatch_adapter.go`
with table-driven unit tests: key building (missing field → terminal 400),
spec passthrough, 501 on import. Copy the invocation-handle wiring from
`GenericAdapter.Provision`/`Delete` (generic.go:142-165) — same
`RequestFuture` + handle pattern, with `json.RawMessage` in place of typed
S/O.

### Stage 2 — loader + registry wiring [SAFE]

1. New env var `PRAXIS_EXTENSIONS_DIR` (add to `internal/core/config/config.go`
   `Load()`, `.env.example`, `docs/OPERATORS.md` env table). Each `*.json`
   file in the dir is one `ExtensionKind`.
2. In `cmd/praxis-core/main.go`, after `provider.NewRegistry(authClient)`:
   load extension records, build `DispatchAdapter`s, and register them.
   Registry needs an additive method — add
   `func (r *Registry) Register(adapter Adapter) error` that rejects
   duplicate kinds (collision with a built-in kind must be a startup error,
   not a silent override).
3. Startup validation: for each extension kind, require a matching `#<Kind>`
   definition under `cfg.SchemaDir` (reuse the engine's schema loading or a
   targeted `cuevalidate`-style lookup) — fail fast with a clear message if
   the operator forgot to mount the schema, since template evaluation would
   reject the kind anyway.

### Stage 3 — CLI awareness [REVIEW]

The CLI's `kindScopes` map (root.go:269) and its guard test assume the
provider registry is the universe of kinds. Decide the minimal surface: `praxis
get/delete <Kind>/<key>` for external kinds needs only kind→scope. Add an
optional `--scope` flag fallback for unknown kinds OR teach the CLI to read
the same extensions dir via an env var. Update
`TestKindScopes_MatchesProviderRegistry` so extension kinds (absent from the
compiled registry) don't fail the guard. This stage changes UX — get review
on the flag design before implementing.

### Stage 4 — docs + example [SAFE]

Update EXTENDING.md's "Future direction" note to present tense; add a worked
example under `examples/` or `docs/` (extension JSON + CUE schema + the
Python driver skeleton EXTENDING.md already shows). Add an integration test
that registers a fake external driver (a test-only Restate service in Go,
bound in the restatetest harness), an extension record, and applies a
template using it end to end.

**Out of scope:** external-kind Plan probes (live describe), import, and
policy-driven validation beyond the CUE schema. Reconciliation is the external
driver's own responsibility (it self-schedules like built-ins — document
this).

---

## R4 — Fleet-scale reconciliation

### Why

At ~1,000 managed resources the current architecture cannot protect AWS or
itself: (1) rate-limit buckets are per-resource-type per-process — the EC2 API
family alone spans ≥13 independent buckets ≈260 rps aggregate against EC2's
~20 rps budget, and every driver-pack replica multiplies it; (2) every
Reconcile calls the **exclusive** `AuthService/<alias>/GetCredentials`
handler, serializing the whole fleet per account; (3) reconcile cadence is
one-size-fits-all (5 min for IAM roles and EKS clusters alike).

### Current state (verified)

- `ratelimit.Shared(name, rps, burst)` — process-local map, first-caller-wins
  config (`internal/infra/ratelimit/limiter.go`); bucket names are
  per-resource-type (`"kms-key"`, `"ec2-instance"`, `"eks-cluster"`, ...;
  only IAM and RDS families share correctly).
- The limiter is baked in at API construction:
  `NewKMSKeyAPI(client)` calls `ratelimit.Shared("kms-key", 10, 5)`; the
  factory closure has `aws.Config` (region, endpoint) in scope:
  `func(cfg aws.Config) KMSKeyAPI { return NewKMSKeyAPI(awsclient.NewKMSClient(cfg)) }`.
- `GetCredentials(ctx restate.ObjectContext, _ string)` is exclusive
  (service.go:75); cache validity = no expiry or ≥5 min remaining
  (service.go:417-429); a `GetStatus(ctx restate.ObjectSharedContext)` shared
  handler already exists on the same VO (service.go:158) — proof that shared
  reads of this state are supported.
- `ReconcileIntervalForKind(string)` ignores its argument
  (`internal/drivers/state.go:32-38`).

### Stage 1 — shared credential fast path [REVIEW]

**Files:** `internal/core/authservice/service.go`, `client.go`.

1. Add a shared-context read handler:

```go
// GetCachedCredentials returns the cached credential when still valid, or
// found=false when the caller must fall back to the exclusive GetCredentials
// handler to refresh. Shared handlers cannot write state, so this never
// refreshes — it only avoids serializing the fleet's cache hits.
func (a *AuthService) GetCachedCredentials(ctx restate.ObjectSharedContext, _ string) (CachedCredentialResponse, error)
```

   `CachedCredentialResponse` = `CredentialResponse` + `Found bool`. Body:
   `restate.Get[*AuthState](ctx, "state")`, then the SAME validity check as
   the exclusive path — but note `isCacheValidAt` takes `now` from
   `journaledNow(ctx)`; confirm `journaledNow` works on
   `ObjectSharedContext` (it should — `restate.Run` is available on shared
   contexts; if not, use the request-time from a plain `restate.Run`).
2. In `RestateAuthClient.GetCredentials` (client.go:40-53): call
   `GetCachedCredentials` first; on `Found`, build the config; on miss, fall
   through to the exclusive `GetCredentials` exactly as today.
3. Verification must include the expiry race: a credential that expires
   between the shared read and use is already tolerated today (5-min validity
   margin) — state that in the code comment.

**Verify:** authservice unit tests (add cache-hit-via-shared,
miss-falls-through, expired-falls-through cases);
`go test -tags integration ./tests/integration/ -run 'TestAuth' `.

### Stage 2 — rate-limit keying by (family, region) [SAFE]

**Files:** `internal/infra/ratelimit/limiter.go`, each driver's `aws.go`
constructor + the factory closures in `driver.go`.

1. Add to ratelimit:

```go
// SharedFor returns the process-wide limiter for an AWS service family in one
// region. AWS throttles per account+region per service; keying the bucket by
// family and region stops N resource types from multiplying one service's
// budget. (Account-level keying needs the account alias at API construction —
// deferred to the harness, R2.)
func SharedFor(family, region string, rps float64, burst int) *Limiter {
	return Shared(family+"|"+region, rps, burst)
}
```

2. Change each driver's API constructor to accept the region and use family
   names, e.g. kmskey `aws.go`:
   `func NewKMSKeyAPI(client *kms.Client, region string) KMSKeyAPI` with
   `ratelimit.SharedFor("kms", region, 10, 5)`, and the factory closure
   becomes `func(cfg aws.Config) KMSKeyAPI { return NewKMSKeyAPI(awsclient.NewKMSClient(cfg), cfg.Region) }`.
   Mechanical across ~51 aws.go files; tests constructing APIs directly need
   the extra argument (compile errors will enumerate them).
3. **Family consolidation table** (the point of the exercise) — one bucket per
   AWS throttle domain: `ec2` (ec2-instance, vpc, subnet, sg, eip, igw,
   natgw, nacl, routetable, vpcpeering, ebs, keypair, ami), `elbv2` (alb,
   nlb, targetgroup, listener, listenerrule), `iam` (already shared),
   `rds` (already shared), `route53` (zone, record, healthcheck), `sqs`
   (sqs, sqspolicy), `sns` (snstopic, snssub), `lambda` (lambda,
   lambdalayer, lambdaperm, esm), `ecr` (ecrrepo, ecrpolicy), `logs`
   (loggroup), `cloudwatch` (metricalarm, dashboard), and one each for kms,
   secretsmanager, ssm, dynamodb, eks, ecs, s3, acm. Budget per family:
   keep the LOWEST rps currently used within the family (safe direction),
   note the chosen numbers in a table in the ratelimit package doc.
4. Document the replica caveat in the package doc: budgets are per-process;
   with N driver-pack replicas the effective budget is N×. Add env override
   `PRAXIS_RATE_LIMIT_SCALE` (float, default 1.0, divide rps by it) so
   operators running replicas can compensate — read once at package init.

**Verify:** `go build ./...` (the signature change finds every call site);
full driver unit suites; one integration smoke
(`-run 'TestS3|TestKMS|TestVPC'`).

### Stage 3 — per-kind reconcile intervals [SAFE]

**File:** `internal/drivers/state.go`.

Implement the switch that `ReconcileIntervalForKind` was built for. Slow-moving
kinds get longer cadence:

```go
var kindIntervals = map[string]time.Duration{
	// Control-plane/identity resources change rarely and are expensive to
	// describe; sub-hourly drift detection buys nothing.
	"IAMRole": 15 * time.Minute, "IAMPolicy": 15 * time.Minute,
	"IAMUser": 15 * time.Minute, "IAMGroup": 15 * time.Minute,
	"IAMInstanceProfile": 15 * time.Minute,
	"Route53Zone": 15 * time.Minute, "ACMCertificate": 15 * time.Minute,
	"KMSKey": 15 * time.Minute,
	"EKSCluster": 10 * time.Minute, "RDSInstance": 10 * time.Minute,
	"AuroraCluster": 10 * time.Minute,
}
```

`ReconcileIntervalForKind` consults the map, falls back to the global var,
clamps to `MinReconcileInterval`. Keep `ReconcileInterval` (the mutable global)
as the base for unlisted kinds so tests keep working. Update the state.go
tests plus `docs/DRIVERS.md`'s reconcile section. The intervals themselves are
a judgment call — flag them for maintainer sign-off in the PR description.

### Stage 4 — load evidence [STOP]

The roadmap framing is "the 5-minute loop stays safe at 1k+ resources" —
that's a measurement, not a code change. Produce a load-test plan (spawn N
S3Bucket resources against Moto, measure reconcile completion time, auth-RPC
p99, limiter backpressure warnings) as a design note for a human to run and
judge. Do not tune numbers without this data.

---

## R5 — Operational telemetry

### Why

The product promise is a continuous reconcile loop, but operators cannot see
it: no `/metrics`, no health endpoint, no first-party metric anywhere (grep
verified). `docs/OPERATORS.md` points only at Restate's own port-9071 metrics
and even claims JSON logging that isn't implemented.

### Current state (verified)

- Each binary runs exactly ONE listener: the Restate h2c endpoint via
  `srv.Start(ctx, cfg.ListenAddr)` (`cmd/*/main.go`). K8s readiness is a bare
  `tcpSocket` probe.
- `prometheus/client_golang` is not a dependency; otel API/SDK v1.39 is in
  the module graph as indirect only.
- Logging is default-handler `slog` (no `SetDefault`, no JSON handler),
  contradicting OPERATORS.md:323.
- Existing shared choke points suitable for instrumentation without touching
  51 drivers: `ratelimit.Limiter.Wait` (already measures wait duration),
  `drivers.ReportDriftEvent` (every drift detection/correction flows through
  it), `EventBus.Emit` (every lifecycle event), `SinkRouter.Deliver`
  (delivery outcomes — sinks already track counters in state),
  `AuthService.GetCredentials` (cache hit/miss).
- The Helm `restate-service.yaml` already names Restate's 9071 port
  `metrics`.

### Design decision

Prometheus `client_golang` with a second HTTP listener per binary. Chosen over
otel-metrics because it's one direct dependency, zero collector
infrastructure, and matches the Restate-9071 scrape model operators already
have. Instrument at the shared choke points only — per-driver metrics come
free later via the harness (R2).

### Stage 1 — telemetry package + /metrics listener in all 7 binaries [SAFE]

1. `go get github.com/prometheus/client_golang` (pin latest stable).
2. New package `internal/infra/telemetry`:

```go
// Package telemetry exposes the process-wide Prometheus registry and the
// standard Praxis metric instruments. Metrics are process-local; Restate
// journal replay can re-execute handler code, so counters incremented inside
// handlers are approximations — increment them inside restate.Run callbacks
// only when exactness matters more than replay cost, and accept
// at-least-once counting otherwise. Document the choice per metric.
package telemetry

func Serve(ctx context.Context, addr string) error // promhttp on /metrics + trivial 200 on /healthz
var (
	EventsEmitted    *prometheus.CounterVec // praxis_events_total{type}
	DriftEvents      *prometheus.CounterVec // praxis_drift_events_total{kind,event} — event: detected|corrected|external_delete
	RateLimitWait    *prometheus.HistogramVec // praxis_ratelimit_wait_seconds{bucket}
	AuthCacheHits    *prometheus.CounterVec // praxis_auth_credentials_total{outcome} — cached|refreshed|error
	SinkDeliveries   *prometheus.CounterVec // praxis_sink_deliveries_total{sink,outcome}
)
```

3. Config: `MetricsAddr` in `internal/core/config/config.go`
   (`PRAXIS_METRICS_ADDR`, default `""` = disabled; document `0.0.0.0:9090`
   as the recommended value). Add to `.env.example` and the OPERATORS.md env
   table.
4. In each of the 7 `cmd/*/main.go`: when `cfg.MetricsAddr != ""`, start
   `telemetry.Serve` in a goroutine before `srv.Start`, with clean shutdown
   on the same signal context.
5. While in main.go: set up JSON logging to make OPERATORS.md's claim true —
   `slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))` at the
   top of every main (or a tiny `telemetry.InitLogging()` helper).

**Verify:** `just up`-style manual check is NOT required; unit-test
`telemetry.Serve` with an `httptest`-style request to `/metrics` and
`/healthz`; build all binaries.

### Stage 2 — instrument the choke points [SAFE, one commit per point]

1. `ratelimit.Limiter.Wait`: observe `time.Since(start)` into
   `RateLimitWait` with the limiter name label (the measurement code already
   exists for the >100ms warning).
2. `EventBus.Emit` (event_bus.go): `EventsEmitted.WithLabelValues(event.Type()).Inc()`
   after successful Append. This runs inside an exclusive VO handler —
   at-least-once on replay is acceptable for a throughput counter; say so in
   a comment.
3. `drivers.ReportDriftEvent` (find it in `internal/drivers/` — it already
   receives ServiceName and the drift event type): increment `DriftEvents`.
4. `AuthService.GetCredentials` + the stage-R4 `GetCachedCredentials`:
   increment `AuthCacheHits` on each branch.
5. `recordSinkDeliveryState` / `deliverToSink` (notification_sinks.go):
   increment `SinkDeliveries` alongside the existing per-sink state counters.

Label-cardinality rule for every metric: labels are bounded sets (kind names,
event types, sink names) — NEVER resource keys, deployment keys, or account
aliases.

**Verify:** unit tests per instrumented package still pass; add one assertion
using `prometheus/client_golang/prometheus/testutil.ToFloat64` where a test
already exercises the path (e.g. drift test → DriftEvents increases).

### Stage 3 — deploy surface [SAFE]

1. Compose: add `PRAXIS_METRICS_ADDR=0.0.0.0:9090` to each praxis service and
   a loopback port mapping per service; use `/healthz` as the compose
   healthcheck the services currently lack (fixes the `just up` registration
   race noted in FABLE §7.3).
2. Helm: add a `metrics` containerPort + Service port to core/driver
   templates; readiness probe switches from `tcpSocket` to `httpGet /healthz`
   on the metrics port when enabled; optional
   `monitoring.serviceMonitor.enabled` template (targets both Praxis services
   and Restate's existing 9071 `metrics` port).
3. OPERATORS.md: replace the aspirational observability section with the real
   metric names and a sample scrape config.

**Out of scope:** tracing, per-resource gauges, dashboards. Event-store depth
and reconcile-loop-lag gauges need a poller design — defer to a follow-up
note.

---

## R6 — Ingress defense-in-depth

### Why

The trust model is documented and honest ("network reachability = admin on
every registered AWS account", OPERATORS.md ~line 85), but there is zero
defense-in-depth: the Helm chart doesn't ship the NetworkPolicy its own docs
demand, and the CLI cannot send the API key that Restate Cloud's ingress
requires — the documented managed-Restate path is unusable from the shipped
CLI.

### Current state (verified)

- sdk-go v0.23.0 supports `restate.WithAuthKey(key)` as an
  `IngressClientOption` — sent as `Authorization: Bearer <key>`
  (sdk internal/ingress/ingress.go:134). Per-request `WithHeaders` exists but
  not client-level arbitrary headers.
- CLI client construction is a single site:
  `NewClient(endpoint)` → `ingress.NewClient(endpoint, restate.WithHttpClient(...))`
  (internal/cli/client.go:80-84). Endpoint precedence: `--endpoint` flag >
  `PRAXIS_RESTATE_ENDPOINT` > `~/.praxis/config.json` > default
  (root.go:116-130).
- One more first-party ingress client: `seedPoliciesFromDir` in
  `cmd/praxis-core/main.go` (`ingress.NewClient(cfg.RestateEndpoint)`).
- Helm templates (12 files) contain no NetworkPolicy; services are ClusterIP;
  compose binds everything to 127.0.0.1.

### Stage 1 — CLI API key [SAFE]

1. `internal/cli/root.go`: add `--api-key` persistent flag with env fallback
   `PRAXIS_API_KEY` and a `APIKey` field in `~/.praxis/config.json`
   (`LoadCLIConfig`), same precedence pattern as the endpoint. Help text:
   "Bearer token sent as Authorization header (Restate Cloud API key, or any
   Bearer-authenticating proxy in front of the ingress)".
2. `internal/cli/client.go` `NewClient`: accept the key and append
   `restate.WithAuthKey(key)` when non-empty. Update `newClient()` in
   root.go:353 to pass it.
3. `cmd/praxis-core/main.go` `seedPoliciesFromDir`: read `PRAXIS_API_KEY` via
   config and pass the same option, so policy seeding works against an
   authenticated ingress. Add the env var to `internal/core/config` +
   `.env.example` (commented) + OPERATORS.md.
4. Never log the key. grep the diff for the flag variable before committing.

**Verify:** CLI unit tests (flag plumbing; a test asserting the option is
applied can use a fake HTTP server checking the Authorization header via
`WithHttpClient`); `docs/CLI.md` global-flags table updated.

### Stage 2 — Helm NetworkPolicy [SAFE]

New `charts/praxis/templates/networkpolicy.yaml` gated on
`networkPolicy.enabled` (values.yaml default **true**), three policies:

1. Praxis core + driver pods: ingress on 9080 allowed ONLY from the Restate
   pod (`component=restate` selector). Nothing else reaches driver packs.
2. Restate pod: 8080/9070 allowed from (a) pods labeled
   `praxis.io/client: "true"` in the release namespace, (b) the registration
   job's pod selector (it must reach 9070 post-install), (c) optionally a
   values-configurable extra `from:` list
   (`networkPolicy.additionalClients: []`) for CI runners in other
   namespaces. Port 9071 allowed from a values-configurable monitoring
   selector.
3. A default-deny for the release's pods is implied by the above being
   non-empty policySelectors — verify against a kind/minikube cluster or at
   minimum `helm template | kubeconform`.

Add a NOTES.txt line telling operators how to label their client workloads.
Update OPERATORS.md's "Add a NetworkPolicy" instruction to "shipped and
enabled by default; disable with networkPolicy.enabled=false".

**Verify:** `helm lint charts/praxis && helm template charts/praxis` in CI
already gates rendering; add a `--set networkPolicy.enabled=false` render to
the CI helm step so both branches stay valid.

### Stage 3 — docs alignment [SAFE]

AUTH.md's security-model section gains a "defense-in-depth options" table:
NetworkPolicy (default on), Restate Cloud API key (CLI `--api-key`), reverse
proxy with Bearer auth (works with the same flag), VPN/tunnel. Keep the trust
model statement unchanged — these layers reduce blast radius; they do not
change the model.

**Out of scope / honest limits (state these in the docs):** self-hosted OSS
Restate ingress performs no caller authentication; `--api-key` is only
enforced by Restate Cloud or a proxy in front. Handler-level shared-token
checks are not feasible through the SDK (handlers don't see HTTP headers) —
if that changes in a future sdk-go, revisit.

---

## Suggested sequencing

R1 stage 1–3 and R6 stage 1–3 are independent and immediately valuable. R5
stage 1 unblocks compose/Helm health checks (which FABLE §7.3 wants anyway).
R2 stage 1 is the big rock — schedule it with a strong model/human, because
R2 stages 2–3 then make R4 stage 2's per-account keying and R5's per-driver
metrics nearly free. R3 can go any time; it only touches the provider/CLI
layer. R4 stage 4's load test should run before *and* after R4 stages 1–3 so
the improvement is measured, not assumed — that's the "data-driven roadmap"
requirement applied to ourselves.
