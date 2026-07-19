# Codex Repository Review

Review target: commit `ae7cfeb` (`2026-07-13` working tree), excluding the untracked
`FABLE.md` and `FABLE_FUTURE.md` from the independent discovery pass.

Companion notes:

- [CODEX_DRIVER_OPTIMIZATION.md](CODEX_DRIVER_OPTIMIZATION.md) — whether generic
  drivers are worth doing and a safe migration shape.
- [CODEX_CORRECTNESS_AND_TESTS.md](CODEX_CORRECTNESS_AND_TESTS.md) — concrete
  correctness findings, verification gaps, and a proposed test standard.
- [CODEX_SECURITY_REVIEW.md](CODEX_SECURITY_REVIEW.md) — trust boundaries,
  sensitive-data paths, deployment hardening, and supply-chain issues.
- [CODEX_LIVE_AWS_TEST_COST.md](CODEX_LIVE_AWS_TEST_COST.md) — a claim-driven
  live-AWS engine acceptance suite, its limits, cost, cadence, and guardrails.

## Bottom line

Praxis has a stronger correctness foundation than most projects at this stage:
single-key atomic driver state, Restate-aware replay discipline in the orchestrator,
typed provider adapters, per-driver CUE schemas, broad Moto integration coverage,
race-enabled unit tests, and explicit crash/fault test infrastructure.

I would not call the current tree correctness-ready for production yet. The highest
risk is not a missing feature; it is that several lifecycle invariants are conventions
copied across 51 drivers rather than properties enforced by a shared kernel and a
conformance suite. That has already produced concrete bugs.

My recommended ordering is:

1. Stop the EC2 KeyPair private-key material from entering deployment state and
   CloudEvents.
2. Fix and regression-test the SNS Topic and Subscription lifecycle defects.
3. Introduce an all-driver lifecycle/error conformance suite before a broad driver
   refactor.
4. Centralize journaled time and AWS error classification.
5. Add the missing network and workload hardening to the Helm chart.
6. Migrate driver lifecycle scaffolding in conformance-tested batches, using an
   explicit state-version bump/reset instead of preserving pre-release JSON shapes.

## Highest-priority current findings

The table below records findings against the reviewed commit. An uncommitted
correctness patch now remediates or narrows the SNS, direct-clock, KeyPair Core-output,
timeout, registry, and selected error-classification items. See the remediation status
at the top of `CODEX_CORRECTNESS_AND_TESTS.md` for the exact fixed/deferred boundary.

| Priority | Finding | Why it matters |
|---|---|---|
| P0 | EC2 KeyPair private key is normalized into generic outputs, then stored in deployment state and emitted in `resource.ready` | A value deliberately excluded from driver state becomes durable and may be forwarded to notification sinks. |
| P0 | SNS Topic and SNS Subscription overwrite the previous desired spec before computing mutable updates | A second `Provision` can return Ready without applying the requested attribute changes. |
| P1 | SNS Topic and Subscription clear their deletion tombstones | Repeated Delete does not reliably remain idempotent and no longer has the state needed for the early-return guard. |
| P1 | Direct `time.Now()` remains in four drivers | Persisted timestamps/conditions can differ on Restate replay. |
| P1 | AWS error policy is not consistently applied | Access-denied and terminal validation failures can be retried many times; transient failures can be made terminal. |
| P1 | Driver timeout cancellation has an external-side-effect ambiguity | AWS may have created a resource before the canceled invocation commits state, leaving an untracked resource. |
| P1 | Helm deploys an unauthenticated trust boundary without a NetworkPolicy or hardened pod security context | Any network peer that reaches Restate ingress has the authority documented in `docs/AUTH.md`, including credential access. |
| P2 | Coverage is collected but not gated; no fuzz/property/mutation layer exists | A green suite does not protect the lifecycle invariants most likely to fail under copy/paste divergence. |

Full evidence and proposed regression tests are in the companion notes.

## Measured local verification

The repository's canonical `just` workflow was exercised after the review:

- `just up` built and started the complete eight-container stack. Moto and Restate
  reported healthy, and Restate listed 70 registered services, including all 51
  resource drivers.
- `just test` passed with `-race`, `-count=1`, atomic coverage, and serial package
  execution. Repository-wide statement coverage was **30.7%**.
- `PRAXIS_INTEGRATION_TIMEOUT=40m just test-integration` passed all runnable cases:
  388 integration tests were discovered and the package completed in **710.683s**
  (11m 50.7s). The suite contains 37 conditional skip sites, chiefly for Moto
  availability or unsupported mutation behavior; an ACM availability skip was
  observed in this run.
- `golangci-lint run ./...` and `go build ./cmd/...` passed.

The green result does not invalidate the static P0/P1 findings. The suite does not
exercise SNS update convergence through a second Provision, SNS double-delete
tombstone retention, or the complete KeyPair output-to-event sensitive-data path.
The run also encountered one Restate Testcontainers startup miss (`port 9070/tcp not
found`) that recovered on retry but added about a minute, providing concrete support
for the proposed test-environment reuse work.

## Pre-release compatibility posture

Praxis is not live and already warns that breaking changes will occur. The review's
recommendations should therefore optimize for a clean, correct v1 contract—not
byte-for-byte compatibility with development-era driver state or internal APIs.

For the generic-driver work, a coordinated breaking change is preferable when it
removes lifecycle ambiguity. Introduce an explicit state-envelope version, update
schemas/adapters/drivers together, and either wipe development Restate state or ship a
single best-effort migration tool for developer convenience. Do not retain deprecated
fields, dual read/write paths, or per-driver compatibility shims unless an actual
pre-release adopter is known to need them.

The safeguards that remain non-negotiable are behavioral: deterministic replay,
atomic state, sensitive-data exclusion, lifecycle/error conformance, and tests that
prove the new state transition/reset path. Those protect correctness; preserving an
old unshipped JSON representation does not.

## Is generic-driver work worth doing?

Yes, but the value is **correctness leverage**, not just fewer lines.

The provider-adapter layer has already proven the approach: 45 of 51 adapter files
use `GenericAdapter[...]` in `internal/core/provider/generic.go`. That is a good
abstraction because it owns invariant dispatch/planning plumbing while descriptors
retain typed resource-specific behavior.

The driver layer should follow the same philosophy, but not become one giant generic
AWS CRUD engine. AWS resources differ materially in identity, eventual consistency,
replacement rules, readiness, import behavior, deletion prerequisites, and drift
semantics. Hiding those differences behind an over-general interface would make
correctness harder to see.

Build a small lifecycle kernel that owns only invariants:

- state transitions and atomic persistence;
- journaled time;
- status/error/condition construction;
- reconcile scheduling and deduplication;
- tombstone and observed-mode deletion rules;
- error classification hooks;
- bounded readiness polling; and
- the eight standard handlers where their semantics truly match.

Keep typed `Observe`, `Create`, `Converge`, `Delete`, readiness, import, and drift
logic in each resource package. First prove the kernel on one simple driver and one
exception-heavy driver. Details are in `CODEX_DRIVER_OPTIMIZATION.md`.

## Repository-wide observations

### What is working well

- There are 51 concrete driver packages, 51 AWS CUE schema files, and 51 driver
  integration-test files. All 51 are present in the provider registry and one of the
  five driver packs.
- All driver packages expose the same eight public handlers: `Provision`, `Import`,
  `Delete`, `Reconcile`, `GetStatus`, `GetOutputs`, `GetInputs`, and `ClearState`.
- The single `drivers.StateKey` pattern is a sound defense against torn driver state.
- The orchestrator generally journals non-determinism and sorts map-derived work.
- CI runs `-race` for the main test suite, lints the integration build tag, checks
  generated event-schema drift, builds all binaries, and renders/lints Helm.
- The linter set includes `staticcheck`, `govet`, `gosec`, `errorlint`, `bodyclose`,
  and `noctx`.
- Fable's earlier retry decoding, sensitivity masking, reconcile jitter, event-schema
  caching, readiness, and CI-hygiene changes are present in the current tree.

### Where conventions have drifted

- `docs/DRIVERS.md`, `docs/PRAXIS_ARCHITECTURE.md`, and
  `skills/review-code/SKILL.md` still describe exactly six handlers. The actual
  contract is eight, and the docs later mention `ClearState` separately. The review
  checklist is therefore incapable of detecting a missing `GetInputs` or
  `ClearState` handler as currently written.
- Only four drivers still use direct wall-clock time, not every driver as stated in
  Fable's original finding. The partial sweep itself is evidence that a textual
  multi-file fix is fragile.
- The documented condition contract remains unevenly implemented across drivers.
- Shared AWS auth classification helpers exist, but are not structurally required at
  AWS-call boundaries.

## Optimization priorities beyond genericization

The most valuable optimizations are those that improve both throughput and
correctness:

1. Replace the workflow's alphabetically selected single-future wait with a
   deterministic, sorted multi-future `WaitFirst`. Current code in
   `internal/core/orchestrator/workflow.go:499` waits only on the selected resource,
   so a slow alphabetically early resource delays processing of already-completed
   work and distorts timeout accounting.
2. Cache and share credentials without serializing every reconcile through the same
   exclusive account object; rate-limit by AWS API family, account, and region.
3. Remove global-object hot spots from fleet indexes and event routing, or shard them
   before expecting hundreds/thousands of resources.
4. Reuse Restate test environments per package where isolation permits it. This can
   cut CI time substantially and make it practical to add stronger fault matrices.
5. Finish converting the six remaining handwritten provider adapters where the
   generic descriptor can express their behavior; extend the descriptor with narrow
   optional hooks rather than retaining 1,798 lines of parallel plumbing.

Do not optimize first by increasing orchestration concurrency. Until throttling,
timeouts, and error classification are conformance-tested, more concurrency mainly
amplifies the least-tested failure modes.

## Fable comparison

I used Fable only after the independent pass. The reviews agree on the broad themes:
driver duplication, missing conformance enforcement, ingress hardening, durable-time
discipline, supply-chain pinning, and stronger property/fault tests.

This review adds or sharpens several current-tree findings not called out in Fable's
main report:

- the KeyPair private key crosses from an ephemeral driver return into durable
  orchestration state and events;
- SNS Provision updates are silently skipped because `state.Desired` is overwritten
  before comparison;
- SNS deletion tombstones are uniquely cleared;
- the current direct-clock problem is isolated to four drivers, not all 51;
- only 13 driver packages appear to apply an auth/access-denied classifier at AWS
  boundaries;
- timeout cancellation needs a provider-side orphan recovery test; and
- Lambda permission `eventSourceToken` lacks sensitive-field metadata.

Fable contains useful additional findings, especially around rollback/event
retention, approval handling, readiness, index scaling, and developer experience.
Those should remain on the backlog, but the P0/P1 items above should precede a large
generic-driver migration.

## Proposed delivery sequence

### Phase 0 — immediate correctness/security fixes

- Remove sensitive/ephemeral outputs from generic state, events, notifications, and
  expression hydration; add a KeyPair end-to-end leak test.
- Preserve old desired state during SNS convergence and retain delete tombstones.
- Replace the four direct clocks with one `drivers.CurrentTime(ctx)` helper.
- Declare `LambdaPermission.spec.eventSourceToken` sensitive.

### Phase 1 — build the safety net

- Add a generated lifecycle conformance suite for every registered driver.
- Add a machine-readable per-kind field/capability manifest.
- Add fault cases for after-provider-create/before-state-commit, throttling,
  access-denied, timeout/cancel, double delete, and explicit clearing of mutable
  fields.
- Enforce a coverage floor and track branch coverage by driver/lifecycle path.

### Phase 2 — narrow generic kernel

- Add shared clock, condition, state-transition, reconcile-scheduling, and error
  helpers first.
- Add an explicit state-envelope version and document the supported development-state
  reset or one-time migration path.
- Migrate one simple driver and validate the new state only; do not require old/new
  byte compatibility.
- Migrate one driver with readiness and one with pre-delete/replacement behavior.
- Once those pilots hold, migrate related driver families in coordinated batches.
  Continue only if the conformance matrix stays green and the resource-specific code
  becomes easier, not harder, to review.

### Phase 3 — production hardening and scale

- NetworkPolicy, API-key/OIDC ingress support, workload identity, pod security
  contexts, encrypted Restate storage, backup/restore drills.
- Real-AWS canary suite for a small, representative resource set.
- Sharded indexes/event routing and account/region/API-family rate limits.
