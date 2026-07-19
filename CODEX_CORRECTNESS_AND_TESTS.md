# Correctness and Test Review

## Uncommitted remediation status

Spark implemented the first correctness tranche under root-agent supervision. The
working tree now contains, but has not committed, these remediations:

- SNS Topic and Subscription Provision compare desired attributes with a fresh AWS
  observation rather than the just-overwritten desired state. Topic tag convergence
  uses that same observation; policy JSON comparisons are semantic.
- SNS Topic and Subscription Delete write a minimal `Deleted` tombstone. This keeps
  repeated Delete idempotent without carrying a stale reconcile-scheduled guard into
  a later reprovision.
- Subscription filter-policy removal uses AWS's documented `{}` representation and
  treats empty/`{}` as equivalent. The broader optional-field tri-state audit remains
  deferred.
- S3, Security Group, SNS Topic, and SNS Subscription now obtain persisted reconcile
  timestamps through the shared journaled `drivers.CurrentTime` helper.
- Registry construction rejects nil, typed-nil, empty, whitespace, and duplicate
  adapter kinds instead of silently dropping or overwriting entries.
- KeyPair private material is excluded from Core-normalized outputs and the CUE
  output contract, so it no longer enters deployment state, resource-ready events,
  notification payloads, or expression hydration. Direct driver invocation and the
  Restate invocation journal still require a separate secure-delivery redesign.
- Workflow timeouts no longer cancel a durable driver invocation whose provider
  outcome may already be committed. The deployment records Error plus Unknown
  Provisioned/Ready conditions, emits timeout evidence, and states that the driver
  continues.
- Four bounded error-policy corrections were applied: DynamoDB limit contention,
  EKS update-in-progress, and EC2 dependency violations during SG/VPC/IGW deletion
  are retryable in their relevant operation contexts. Generic plan probes now map
  expired credentials, authorization, and validation errors terminally while leaving
  throttling, transport, and provider 5xx failures retryable.

This does **not** close the repository-wide error-classification or ambiguous-create
problem. A managed-key-only EC2/EBS client token was considered and rejected because
reusing it across an intentional recreate/replacement can cause idempotency mismatch
or stale-operation reuse. Correct provider idempotency needs a durable per-create
attempt identity in the lifecycle state/kernel.

Existing focused race tests, lint, formatting, KeyPair CUE validation, and all command
builds pass. The systematic regression/conformance test expansion remains the next
separate pass, per the requested implementation-first sequence.

## Concrete correctness defects

### P0 — SNS Provision updates compare desired state with itself

SNS Topic stores the incoming spec at `internal/drivers/snstopic/driver.go:74`, then
passes `spec` and `state.Desired` to `convergeAttributes` at line 139. Both arguments
are now the same value, so comparisons at lines 198–215 produce no attribute updates.

SNS Subscription repeats the defect: assignment at
`internal/drivers/snssub/driver.go:73`, comparison at line 103.

Impact: a second `Provision` for an existing resource can return Ready without
applying mutable attribute changes. Reconcile may repair the state later, but the
Provision contract has already reported success incorrectly.

Fix: retain `previousDesired := state.Desired` before overwriting it, or preferably
compare desired directly with the freshly observed provider state. The latter is more
robust after crashes and external changes.

Required regression tests:

- Provision a topic, update every mutable attribute, Provision again, and Describe
  AWS before any reconcile.
- Do the same for Subscription attributes.
- Assert the second Provision is a no-op when values are equal.
- Exercise explicit clearing, not only non-empty replacement.

### P1 — SNS deletion tombstones are erased

SNS Topic writes `StatusDeleted` and immediately clears the same key at
`internal/drivers/snstopic/driver.go:344-346`. SNS Subscription does the same at
`internal/drivers/snssub/driver.go:372-374`. These are the only normal driver Delete
handlers that call `restate.Clear(ctx, drivers.StateKey)`.

On a second Delete, the early tombstone check sees a zero-value state and the handler
continues with an empty account/resource identifier. The outcome then depends on
empty-account resolution and provider validation instead of guaranteed idempotency.

Fix: retain the Deleted tombstone. Reserve `ClearState`/`ClearAllState` for explicit
orphan/adoption reset semantics.

Required tests:

- Delete twice after a successful Provision.
- Delete twice after external deletion.
- GetStatus after Delete returns Deleted.
- Re-provision after Delete follows the documented generation/adoption semantics.

### P1 — direct wall-clock access remains outside `restate.Run`

The remaining sites are:

- `internal/drivers/s3/driver.go:398,402,413,421,432`
- `internal/drivers/sg/driver.go:307,315,324`
- `internal/drivers/snstopic/driver.go:361`
- `internal/drivers/snssub/driver.go:389`

These values are written into state and conditions. Re-executing handler code during
journal replay can therefore produce a different value.

Fix: one `drivers.CurrentTime(ctx)` helper returning a journaled timestamp. Add a
static analysis check (or focused linter script) that rejects `time.Now`, randomness,
environment reads, and unordered map-derived journal calls in driver handlers unless
inside `restate.Run`.

### P1 — AWS error classification is structurally incomplete

`internal/drivers/autherr.go` provides `TerminalAuthError` and `ClassifyAPIError`, but
only SQS and SQSPolicy call `ClassifyAPIError`; no `driver.go` calls
`TerminalAuthError` directly. Counting shared/local access-denied checks together,
only 13 of 51 driver packages appear to classify auth errors at AWS boundaries.

Consequences:

- terminal access-denied/invalid requests can be returned bare from `restate.Run` and
  retried up to the runtime limit;
- transient provider/network errors can be wrapped terminally;
- status codes vary for the same failure across kinds; and
- plan-time probes in `internal/core/provider/generic.go:199-208` treat only
  throttling as retryable and make every other describe error terminal 500.

Fix: centralize operation-aware classification and require every AWS `restate.Run`
closure to pass errors through it. Classification needs operation context because the
same AWS code can mean conflict on Create and transient in-progress state on Update.

Conformance cases for every kind:

| Provider failure | Expected behavior |
|---|---|
| AccessDenied / UnauthorizedOperation | Terminal 403, no repeated AWS calls |
| Validation / invalid parameter | Terminal 400/422 |
| NotFound during observe | Resource-specific absence result, not generic failure |
| NotFound during idempotent delete | Success |
| AlreadyExists / immutable conflict | Terminal 409 where replacement is supported |
| Throttling / limit-in-progress | Retryable with bounded backoff |
| Network timeout / provider 5xx | Retryable |
| Exhausted retry budget | Stable Error with actionable cause |

### P1 — timeout cancellation can orphan provider resources

At `internal/core/orchestrator/workflow.go:521-529`, a timed-out driver invocation is
canceled and the resource is marked failed. Cancellation cannot reverse an AWS side
effect that already succeeded. If Create succeeded but the driver had not yet
committed outputs/state, the deployment can lose the identifier needed to adopt or
delete the resource.

This is an unavoidable distributed-systems window, so it needs an explicit recovery
policy, not just a longer timeout.

Recommended behavior:

- Treat cancellation as "stop waiting" rather than proof that no provider resource
  exists.
- Require deterministic ownership tags/tokens on create where AWS supports them.
- On retry/recovery, Observe by stable identity/managed key before creating again.
- Emit a distinct `UnknownOutcome` condition and surface an operator recovery path.
- Add crash/timeout tests at the exact point after provider create and before state
  commit for representative resource families.

### P2 — optional fields lack explicit-clear semantics

For SNS Topic, Policy, DeliveryPolicy, and KMS key changes are emitted only when the
desired value is non-empty (`internal/drivers/snstopic/driver.go:201-208`), and drift
logic similarly ignores empty desired values. This makes omission and "clear this
provider value" indistinguishable.

Audit every optional mutable field as one of:

- unmanaged when absent;
- managed with a default when absent; or
- explicitly clearable via pointer/nullable/tri-state input.

Capture that decision in the per-kind capability/field manifest and test all three
states where applicable.

### P2 — duplicate registry kinds overwrite silently

`NewRegistryWithAdapters` assigns `byKind[adapter.Kind()] = adapter` at
`internal/core/provider/registry.go:224-234`. A duplicate silently replaces the first
adapter. Startup should fail on nil/empty/duplicate kind, and a generated inventory
test should compare schema kinds, registry kinds, driver services, pack bindings, and
integration fixtures.

## Test-suite assessment

### Strong coverage foundations

- 51 driver integration-test files correspond to 51 driver/schema kinds.
- Real Restate is used through Testcontainers in handler/integration tests.
- Crash-resume, approval crash, retry, and max-parallel fault infrastructure exists.
- Main tests run with `-race`, `-count=1`, atomic coverage, and `-p 1`.
- Integration-tag code is included in golangci-lint.
- Moto is seeded in CI and integration is release-gating.
- Event schema generation drift and Helm render/lint are CI gates.

### What the green suite does not currently prove

- There is no coverage threshold; `coverage.out` is uploaded regardless of result.
- There are no Go fuzz targets, property tests, or mutation tests.
- Many driver tests exercise pure drift/spec helpers but not the handler path that
  connects persisted previous state, AWS mutation, refresh, and returned status.
- There is no all-driver negative-path conformance matrix.
- Mutable fields are not systematically tested one-by-one through
  Provision → AWS Describe.
- Explicit clearing is rarely tested.
- No test traces sensitive data across driver output → adapter normalization →
  deployment state → CloudEvent → notification sink.
- No timeout/cancel test forces the provider-side ambiguous-outcome window.
- Moto cannot validate real AWS IAM, eventual consistency, quotas, waiter behavior,
  TLS, KMS, or service-specific error details.
- Helm is rendered but never booted; chart Restate defaults to 1.3 while Compose uses
  1.6, so compatibility is not exercised.

## Recommended verification architecture

### 1. Generated inventory gate

Create one test/tool that fails if these sets differ:

- CUE resource definitions;
- provider registry kinds;
- Restate service names;
- driver-pack bindings;
- adapter descriptors;
- integration-test fixtures; and
- sensitive-field declarations.

This replaces hand-maintained counts and catches silent duplicate overwrites.

### 2. Lifecycle conformance harness

Run the same invariant suite against every descriptor. Use an in-memory mock API for
deterministic failure injection plus a Restate server for actual handler/replay
behavior. The harness should be data-driven by capabilities, not skipped ad hoc.

Minimum matrix:

| Area | Required assertions |
|---|---|
| Provision | create, second identical call, every mutable update, external disappearance |
| State | legal status transitions, generation rules, atomic envelope, no sensitive output |
| Import | Observed default, Managed option, missing provider resource |
| Delete | normal, double, provider already absent, Observed guard, tombstone retained |
| Reconcile | no drift, drift detection, correction, audit mode, external delete, deduped timer |
| Errors | auth, validation, conflict, throttling, network/5xx, retry exhaustion |
| Replay | crash before call, after call/before state, after state/before response |
| Time | all persisted timestamps journaled and monotonic enough for the contract |

### 3. Field-level contract tests

Maintain a per-kind table declaring each spec field as identity, immutable, mutable,
computed/defaulted, sensitive, clearable, or ignored/unmanaged. Generate tests that:

- change one field at a time;
- verify plan operation and masked diffs;
- invoke Provision;
- Describe provider state;
- verify GetInputs/GetOutputs/GetStatus; and
- reconcile an external mutation.

This would have caught the SNS self-comparison bug immediately.

### 4. Fault-window tests

Extend the crash probe so the failure is injected around real driver operations:

1. before provider Create;
2. after provider Create, before state Set;
3. after state Set, before response;
4. while waiting for readiness;
5. after Delete, before tombstone; and
6. during drift correction.

Assert no duplicate creates, no lost identifier, deterministic replay, and a usable
operator recovery path for irreducibly ambiguous outcomes.

### 5. Real-AWS canary tier

Moto should remain the broad, fast integration tier. The live tier should validate
Praxis engine mechanics against provider truth, not attempt to certify all 51
drivers. Use a dependency-rich set of about ten kinds: VPC, Subnet,
InternetGateway, RouteTable, SecurityGroup, IAMRole, IAMInstanceProfile,
EC2Instance, S3Bucket, and KMSKey.

That topology can test real output hydration, DAG fan-out/fan-in, IAM propagation,
network dependencies, asynchronous readiness, no-op apply, mutable update, external
drift, observed import, crash/replay windows, reverse-order deletion, and independent
leak detection. Keep rare-error generation and exact failure-boundary tests in the
deterministic harness, but repeat representative cases with real AWS mutations.

Use a dedicated account, least-privilege Praxis role, separate verifier/cleanup
role, strict cost/tag policy, unique run ID, durable resource manifest, and an
out-of-band sweeper that reports leftovers. The complete acceptance matrix, proof
limits, topology diagrams, and revised **$25–$100/month** operating envelope are in
`CODEX_LIVE_AWS_TEST_COST.md`.

## CI recommendations

1. Add a coverage no-regression threshold now, then ratchet branch coverage for core
   lifecycle packages. Avoid a single repository-wide percentage as the only goal.
2. Add `govulncheck ./...` and dependency/container/chart scanning.
3. SHA-pin GitHub Actions and version-pin the CUE CLI installation.
4. Add nightly `-race` integration if it is too slow for every pull request.
5. Make Moto availability fatal in CI; availability-based skips must not green out a
   resource family.
6. Shard integration by driver pack after sharing Restate environments per package.
7. Boot the Helm chart in Kind/k3d and execute registration, health, one plan/apply,
   and backup/restore smoke tests.
8. Test the supported Restate version matrix or use one pinned version everywhere.

## Local verification performed for this review

- `golangci-lint run ./...` — passed, 0 issues.
- `go build ./cmd/...` — passed.
- `just up` — passed. The canonical recipe built and started all eight Compose
  services; Moto and Restate were healthy, and Restate reported 70 registered
  services, including the 51 resource drivers. The stack was left running.
- `just test` — passed using the exact recipe flags: `-race`, `-count=1`, `-p 1`,
  `-covermode=atomic`. No race was reported. `coverage.out` contains **30.7%** total
  statement coverage.
- Coverage is uneven in exactly the lifecycle-heavy area where correctness matters.
  Representative low driver-package results include KeyPair 7.4%, ElasticIP 8.5%,
  VPC 10.2%, VPCPeering 10.4%, SQSPolicy 11.2%, IAMPolicy 11.4%, SQS 13.1%, IAMGroup
  13.4%, IAMUser 13.8%, LambdaLayer 14.3%, and IAMRole 15.8%. By contrast,
  `cuevalidate` reached 97.3%, workspace 92.9%, and driver common helpers 90.9%.
- `PRAXIS_INTEGRATION_TIMEOUT=40m just test-integration` — passed. The package
  exposes **388 integration tests** and completed in **710.683s** (11m 50.7s).
- The integration source contains 37 conditional skip sites, including four
  short-mode gates and numerous Moto availability/unsupported-update gates. This
  run visibly skipped ACM status coverage when Moto returned an unsupported 500
  response. A passing package therefore must not be read as 388 fully exercised
  AWS behavior checks.
- One Restate Testcontainers startup attempt failed because port `9070/tcp` was not
  found. The harness retried successfully and the affected test passed, but the
  recovery added about 60 seconds. Sharing environments per package/driver pack
  would reduce both runtime and this source of flake.
- A non-escalated `just status` cannot reach the Docker socket in the execution
  sandbox; the same repository command outside that sandbox confirmed the full
  stack remained healthy after both suites.

The static SNS and KeyPair findings are not inferred from failing tests; they follow
directly from the current data flow and are precisely the gaps the current passing
tests do not exercise.
