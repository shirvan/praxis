# FABLE.md — Repository Review

A full-repo critique covering security, correctness, verifiability, testing, performance,
and developer experience. Produced by a multi-agent review (five parallel deep reviews:
security, core architecture/performance, driver layer, testing/CI, DX/docs), with the
highest-impact claims re-verified by hand against the code. Every finding cites
file:line and comes with a concrete recommended fix.

**Overall verdict:** this is an unusually disciplined alpha. Replay-safety in the
orchestrator, the single-atomic-state-key driver pattern, crash-resume and
fault-injection test harnesses, an honest documented threat model, and a real
integration-gated release pipeline are all things most projects at this stage don't
have. The serious problems cluster in four places: **(1)** secret material leaks
through the create-plan and `GetInputs` paths, **(2)** the orchestrator's retry
machinery for throttled resources is effectively dead code, **(3)** the newest driver
generation (EKS/DynamoDB/etc.) reports Ready before resources exist and has diverged
from the driver contract, and **(4)** ~70% of each driver is copy-pasted boilerplate,
which is the root cause of (3) and will make every cross-cutting fix a 45-file edit.

---

## Contents

1. [Critical findings](#1-critical-findings)
2. [Security](#2-security)
3. [Correctness & durability](#3-correctness--durability)
4. [Performance & scale](#4-performance--scale)
5. [Driver layer](#5-driver-layer)
6. [Testing & CI](#6-testing--ci)
7. [Developer experience & docs](#7-developer-experience--docs)
8. [What's working well](#8-whats-working-well)
9. [Prioritized action plan](#9-prioritized-action-plan)

---

## 1. Critical findings

### 1.1 Secret values leak into plan output, saved plans, and `praxis get` — **HIGH (security)**

Masking exists only on the *update* path. The drivers correctly emit `(sensitive)` in
update-path field diffs (`internal/drivers/secret/drift.go:50`,
`internal/drivers/ssmparameter/drift.go:64`), and `Rendered` masks SSM-resolved values.
But:

- **Create path:** `planCreate` (`internal/core/provider/generic.go:244-251` →
  `createFieldDiffsFromSpec` in `internal/core/provider/helpers.go:114-132`) flattens
  the entire spec into field diffs verbatim — including `spec.secretString`
  (Secrets Manager), `spec.value` (SSM SecureString), and `spec.masterUserPassword`
  (RDS/Aurora, which have **no** masking at all). These plaintext values flow into
  `praxis plan` terminal output (⇒ CI logs) and saved plan files
  (`saved_plan.go:44` copies the raw SSM-resolved spec). The `compiled.Sensitive`
  path set (`internal/core/command/pipeline.go:253-268`) is tracked but never
  consulted for field diffs or the execution plan.
- **GetInputs:** `praxis get <deployment> --inputs` — and *any* `-o json` get
  (`internal/cli/get.go:354` always fetches inputs in JSON mode) — calls the drivers'
  `GetInputs`, which returns `state.Desired` unmasked
  (`internal/drivers/secret/driver.go:326-332`). This directly contradicts the design
  comment in `internal/drivers/secret/types.go:8` ("it never leaves the driver as
  output").

**Fix:**
1. Add `SensitiveFields []string` to `GenericDescriptor` (e.g.
   `{"spec.secretString"}`, `{"spec.masterUserPassword"}`) and have
   `createFieldDiffsFromSpec` replace those paths with `(sensitive)`.
2. In `computeResourceDiffs`, apply `compiled.Sensitive` masking to all `FieldDiff`
   values and to `ExecutionPlan.Resources[].Spec` before returning `PlanResponse`
   (keep the unmasked spec only in the workflow-submission payload).
3. In sensitive drivers' `GetInputs`, blank the sensitive field before returning —
   drift detection doesn't need the raw value (see §2.2).

### 1.2 Orchestrator retry for throttled resources is dead code — **CRITICAL (correctness)**

`decodeRetryableInvocationError` (`internal/core/provider/helpers.go:66-86`) is the
fallback that reconstitutes retryable errors after they cross a Restate RPC boundary.
It requires **both** error code 425/429 **and** the literal substring `"retryable"` in
the message. Drivers signal throttling/limits as `restate.TerminalError(rawAWSErr, 429)`
with raw AWS messages (e.g. `internal/drivers/ebs/driver.go:180` modification cooldown,
`internal/drivers/snssub/driver.go:143` SubscriptionLimitExceeded) — which never
contain "retryable". Result: `IsRetryable` returns false, the resource is marked
`Error`, all transitive dependents are **Skipped**, and the entire backoff/retry
machinery (`backoff.go`, `markRetrying`, `lifecycle.maxRetries`) never fires.
`docs/ORCHESTRATOR.md` ("Retry and Backoff") describes behavior the code doesn't
deliver.

**Fix:** drop the message heuristic. Treat any terminal error with code 429 (and
425/503) as retryable regardless of message. If `RetryAfter` needs to survive the
boundary, encode it in a structured message suffix and document the convention in the
driver contract.

### 1.3 Eventing is a synchronous, globally serialized bottleneck that can wedge deployments — **CRITICAL (performance + correctness)**

Every lifecycle event from every workflow funnels through blocking `.Request()` calls
to `EventBus` keyed `"global"` (`internal/core/orchestrator/runtime.go:573-598`), whose
**exclusive** handler (`event_bus.go:53-91`) synchronously calls
`DeploymentEventStore.Append` (a second hop) — and validation
(`internal/core/cuevalidate/validate.go:63-90`) does `os.ReadFile` +
`cuecontext.New()` + `CompileBytes` **per event**. Three problems:

- One exclusive virtual object is the throughput ceiling for all concurrent
  deployments.
- Fresh CUE context + schema compile per event costs tens of ms each.
- `Emit` failures propagate: `workflow.go:753-755` returns the error from the
  workflow, aborting a deployment mid-flight **without finalizing** — it sticks at
  `Running` until the delete-workflow drain heuristic force-cancels it.
  `docs/PRAXIS_ARCHITECTURE.md` claims "fires events and forgets about them"; the code
  blocks on two exclusive hops.

**Fix (three independent pieces):**
1. Cache compiled CUE schema values in `cuevalidate` (`sync.Map` keyed by
   `path|definition`; schemaDir is immutable).
2. Remove the global EventBus hop — sequencing is already single-writer per deployment
   inside `DeploymentEventStore.Append`; a stateless service can validate and
   `ObjectSend` directly to the per-deployment store.
3. Emit from workflows via `ObjectSend` (fire-and-forget), or at minimum
   log-and-continue on emission errors instead of returning them from `Run`.

### 1.4 EKS/DynamoDB report Ready while still creating; other waiters aren't durable — **HIGH (correctness)**

Three waiting styles coexist, plus outright gaps:

| Style | Where | Problem |
|---|---|---|
| No wait at all | `internal/drivers/ekscluster/driver.go:125-135` sets `StatusReady` immediately after `CreateCluster` (cluster is `CREATING` for 10–15 min); DynamoDB same pattern; no `WaitReady` in `internal/core/provider/ekscluster_adapter.go` | Dependents (nodegroups, ESMs on a DDB stream) fail immediately after a "successful" provision. Compounds with §5.3: the first reconcile sees a `CREATING` cluster, calls `UpdateClusterConfig` → `ResourceInUseException` → terminal 409 → resource stuck in `Error` forever. |
| Blocking SDK waiter / `time.Sleep` loop **inside one `restate.Run`** | `internal/drivers/ec2/aws.go:208-213` (5 min), `internal/drivers/natgw/aws.go:117-131` (10 min), `internal/drivers/lambda/aws.go:284-303` (2 min sleep loop) | Not durable — a crash mid-wait re-executes the whole wait; the virtual object's exclusive lock is held the entire time; a 10-minute Run with zero journal activity risks Restate's inactivity handling killing the invocation. |
| Durable `restate.Sleep` loop, **unbounded** | `internal/drivers/alb/driver.go:456-474`, nlb equivalent | An ALB stuck in `provisioning` loops forever, journal grows every 10s. |

The correct pattern already exists: the orchestrator's `ReadyWaiter` + durable
`restate.Sleep` poll (`internal/core/orchestrator/wait.go:44-72`), used by ec2, rds,
lambda, aurora adapters.

**Fix:** for EKS and DynamoDB, add a bounded durable wait *inside the driver's
Provision* modeled on `alb/driver.go:456` `waitForActive` (one `Describe` per
`restate.Run`, `restate.Sleep` between attempts) before setting `StatusReady`. Do
**not** implement this as an adapter-side `WaitReady` polling `GetOutputs` — the
driver stores outputs captured at create time (`CREATING`) and only refreshes them on
the 5-minute reconcile, so the adapter would poll stale state. Convert the
ec2/natgw/lambda blocking waits to the same shape, and bound *all* such loops with max
attempts (including the currently unbounded alb/nlb ones). See Appendix A3 for the
exact code shape.

### 1.5 ~70% of every driver is duplicated boilerplate — **HIGH (maintainability, root cause of contract drift)**

Diffing `dynamodbtable/driver.go` (539 lines) against `ekscluster/driver.go` (556
lines) after mechanical type renaming leaves only ~183 differing lines. The clones
include the full Provision scaffold, Import, Delete, the ~70-line Reconcile state
machine, `GetStatus`/`GetOutputs`/`GetInputs`, `scheduleReconcile`, `apiForAccount`,
`failProvision`, `tagDiff`/`managedTags`, `computeTagDiffs` (~40 copies),
`stringSetEqual`, `FieldDiffEntry` (declared ~45 times), and `policiesEqual`
(byte-identical in sqspolicy/snssub/snstopic). Estimated ~20k of ~56k driver LOC is
removable skeleton.

The cost is already visible as behavioral drift: only S3 implements the Conditions
contract that `docs/DRIVERS.md` says every driver must implement (§5.1); external
delete is reported differently across generations; immutable-change handling diverges
(§5.4).

**Fix:** a generics-based harness — every driver already has the same shape:

```go
// internal/drivers/harness/harness.go
type ResourceOps[S, O, Obs any] interface {
    Validate(S) error
    ApplyDefaults(S) S
    Observe(restate.RunContext, S) (Obs, bool, error)
    Create(restate.RunContext, S) (Obs, error)
    Converge(ctx restate.ObjectContext, desired S, observed Obs) error
    HasDrift(S, Obs) bool
    Outputs(Obs) O
    SpecFromObserved(Obs) S
}
```

Provision/Import/Delete/Reconcile/GetStatus/GetOutputs/GetInputs implemented once on
`Driver[S, O, Obs]`; per-driver packages shrink to types.go + aws.go + drift.go + a
`harness.New(...)` call. Restate's `restate.Reflect` discovers handlers by method, so
generic methods register fine. Migrate incrementally, newest drivers first (they're
already byte-compatible with each other). This structurally fixes §5.1 and §5.4 and
gives one place to install bounded durable waiters (§1.4) and shared error policy
(§5.3).

---

## 2. Security

The declared trust model (`docs/AUTH.md:17-37`) is explicit and honest: the Restate
ingress is the trust boundary; anyone who can reach port 8080 can extract AWS
credentials. Findings are graded *within* that model — leaks beyond the boundary rank
higher than things the model already concedes.

### 2.1 No optional auth layer; Helm chart ships no NetworkPolicy its own docs require — MEDIUM

`docs/OPERATORS.md:83-90` tells operators to "Add a NetworkPolicy restricting
traffic" — but `charts/praxis/templates/` contains no `networkpolicy.yaml`. The CLI
also cannot send an `Authorization` header (`internal/cli/client.go:80` sets no auth
options), so the documented "use Restate Cloud (API-key ingress)" escape hatch is
unusable from the shipped CLI.

**Fix:** (1) add a `networkPolicy.enabled` value + template, defaulting on; (2) add
`PRAXIS_API_KEY` / `--api-key` bearer-header support to the CLI; (3) longer-term, gate
`AuthService.GetCredentials`/`Configure` behind an optional pre-shared token.

### 2.2 Secrets and static AWS credentials persist in plaintext in Restate state/journal — MEDIUM

`AuthState.CachedCredential` (with `secretAccessKey`) is `restate.Set` in
`internal/core/authservice/service.go:271-275,318-323`; the secret driver stores both
`Desired.SecretString` *and* `Observed.SecretString`
(`internal/drivers/secret/types.go:44-52`), fetching the live value via
`GetSecretValue` on every reconcile (`internal/drivers/secret/aws.go:118`). All of it
is journaled by Restate's durable log on the `restate-data` volume — retrievable via
Restate introspection APIs.

**Fix:** store `SHA-256(SecretString)` for drift comparison instead of the raw value
(hash inside the `restate.Run` closure before returning). Document in OPERATORS.md
that Restate's journal retains credential responses; recommend volume encryption and
journal retention limits.

### 2.3 `DeleteSecret` uses `ForceDeleteWithoutRecovery` unconditionally — MEDIUM

`internal/drivers/secret/aws.go:168`. Every delete — including one caused by a
mistaken template edit — permanently destroys the secret with no recovery window
(AWS default is 30 days). Also, immediately recreating the same name hits
`InvalidRequestException` ("scheduled for deletion"), which `IsInvalidParam`
(`secret/aws.go:210-212`) makes terminal.

**Fix:** default to `RecoveryWindowInDays: 7`; add `spec.forceDelete: bool` for
opt-in immediate deletion (Moto/tests); classify the scheduled-for-deletion recreate
error as retryable.

### 2.4 CUE evaluation of untrusted payloads has no resource limits or timeout — MEDIUM

Templates arrive as raw bytes over the ingress and are evaluated synchronously
(`internal/core/template/engine.go:139-291`) with no size cap and no timeout. CUE
comprehensions can blow up exponentially (`list.Range`-driven comprehensions producing
millions of resources) and will pin CPU/OOM `praxis-core` (256Mi limit in the chart).
Policies multiply the cost (`engine.go:244` unifies once per policy). The file-access
surface is fine — virtual overlay, no `@embed`.

**Fix:** reject templates/policies over ~1 MiB with `restate.TerminalError(..., 413)`;
run evaluation under a `context.WithTimeout` (~30s) in a goroutine, returning terminal
422 on timeout; adopt CUE v0.16 evaluator limits via `cuecontext` options.

### 2.5 Supply chain: mutable action tags, no govulncheck, no dependabot, `latest` images — MEDIUM

All GitHub Actions are tag-pinned (`softprops/action-gh-release@v2`,
`extractions/setup-just@v2`, etc.); a compromised third-party tag in `release.yml`
runs with `contents: write`/`packages: write`. No `govulncheck` in CI (gosec covers
code patterns, not dependency CVEs); no `.github/dependabot.yml`.
`motoserver/moto:latest` and `amazon/aws-cli:latest` in compose; Helm
`global.imageTag: latest`; chart pins Restate `1.3` while compose uses `1.6`.

**Fix:** pin actions to commit SHAs with a version comment; add a
`govulncheck ./...` CI step; add dependabot config covering `gomod`,
`github-actions`, `docker`; stamp the Helm default tag at release time (the chart
version already is); bump chart Restate to 1.6.

### 2.6 Notification sinks: unredacted stored credentials; unrestricted webhook SSRF — MEDIUM

Sink `Headers` (typically `Authorization` tokens) are persisted and returned
unredacted by server-side `Get`/`List`; only the CLI masks them
(`internal/cli/notifications.go:119-133`). Any ingress caller can register a webhook
pointed at internal endpoints (e.g. `http://169.254.169.254/...`) and have
`praxis-core` POST event payloads there
(`internal/core/orchestrator/notification_sinks.go:574-601`).

**Fix:** redact literal header values server-side unless they're `ssm:///` references
(the resolution machinery at `notification_sinks.go:510-565` already supports refs —
that's the right pattern); enforce `https://` and reject link-local/loopback/RFC-1918
sink targets unless an operator flag allows it.

### 2.7 Low-severity security items

- **Helm securityContext:** zero `securityContext` blocks in any template. Praxis
  images are distroless nonroot (good), but the Restate statefulset and registration
  job lack `runAsNonRoot`/`capabilities: drop: [ALL]`/`seccompProfile` — required by
  PSA `restricted`. Add a standard block to all four templates.
- **`moto-init/setup.sh`:** add a guard refusing to run unless `AWS_ENDPOINT_URL` is
  set, so the seed script can never hit a real account.
- **AssumeRole session names** use second-granularity timestamps
  (`internal/core/authservice/sts.go:56-59`) — collisions muddy CloudTrail
  attribution. Append the account alias and a unique suffix.
- **`PRAXIS_PLAN_SIGNING_KEY`** (`internal/cli/plan.go:141`) deserves doc prominence:
  unsigned saved plans are the default and the HMAC protection (well implemented,
  including rejecting unsigned plans when a key is set) is opt-in.

---

## 3. Correctness & durability

### 3.1 Head-of-line blocking in the dispatch loop — HIGH

`nextInFlightCompletion` (`internal/core/orchestrator/inflight.go:23-30`, consumed at
`workflow.go:507-538`; same pattern in `delete_workflow.go:339-359`) picks the
*alphabetically first* in-flight resource and waits on that one future only. If
`aaa-rds` takes 25 minutes and `zzz-record` finishes in 2 seconds, `zzz`'s dependents
can't dispatch until `aaa-rds` completes. Parallelism collapses to worst-case chain
behavior — contradicting ORCHESTRATOR.md's "WaitFirst returns whichever completes
first" — and per-resource timeouts measure from head-wait start, not dispatch.

The comment at `workflow.go:512-517` blames a journal-mismatch (code 570) on
`WaitFirst` over map-derived futures — but the non-determinism was map iteration
order, not `WaitFirst` itself.

**Fix:** build the future list from **sorted** in-flight names each iteration and pass
all of them to `restate.WaitFirst`; identify the winner by comparing against the
sorted slice — deterministic on replay, restores eager completion. Track per-resource
deadlines as absolute journaled timestamps.

### 3.2 `time.Now()` outside `restate.Run` in every driver — MEDIUM

E.g. `internal/drivers/s3/driver.go:398,402,413,421,432` (`state.LastReconcile`,
condition timestamps); the pattern exists across all drivers. Wall-clock values
recomputed during journal replay can diverge from the original execution — the
documented Restate anti-pattern. The orchestrator gets this right
(`runtime.go:454-460`); drivers don't.

**Fix:** add a `drivers.Now(ctx)` helper wrapping `restate.Run` (mirroring
`orchestrator.currentTime`) and sweep drivers. Falls out free with the harness (§1.5).

### 3.3 Retention pruning can silently break rollback — MEDIUM

`RollbackPlan` is built purely from `resource.ready` events
(`internal/core/orchestrator/event_store.go:207-243`); `Prune`
(`event_store.go:249-325`) deletes whole chunks by age/count. After a sweep,
`DeploymentRollbackWorkflow` silently skips resources whose ready events were pruned —
orphaned cloud resources with no error.

**Fix:** derive the rollback plan from `DeploymentState.Resources` (authoritative,
never pruned) using stored statuses + reverse topo — the DAG is already
reconstructable via `planResourcesFromState`. Alternatively have `Prune` record a
"rollback horizon" that rollback surfaces as an explicit error.

### 3.4 Docs claim at-least-once sink delivery; implementation is at-most-once — MEDIUM

`docs/EVENTS.md:482` vs `notification_sinks.go:296-299` (circuit open ⇒ events
silently skipped, never replayed), `:408-429` (bounded attempts then drop), `:357-368`
(restate_rpc sinks marked `Succeeded` immediately after a one-way `Send` — the success
counter is fiction).

**Fix:** either correct the docs ("best-effort with bounded retries; circuit-open
drops") or add a per-sink durable dead-letter queue storing skipped sequence ranges,
replayed on circuit close via the existing `ListSince`/`GetRange`.

### 3.5 Approval gate: any awakeable error is recorded as a human rejection — MEDIUM

`workflow.go:133-138` converts *any* `approval.Result()` error — including transport
failures — into `ApprovalDecision{Approved: false}` and permanently cancels the
deployment, emitting an `approval.rejected` audit event with `DecidedBy: ""`.

**Fix:** the command service should resolve the awakeable with a rejection *value*,
never reject the awakeable itself; treat awakeable rejection/errors as unexpected and
return them so Restate retries/resumes the suspension.

### 3.6 Smaller correctness items — LOW

- **`Error` status never recovers.** Reconcile in `Error` is describe-only
  (`s3/driver.go:426-430`) even when describe shows the resource healthy and
  drift-free; PRAXIS_ARCHITECTURE.md's state diagram shows `Error → Ready: recovery`.
  Implement recovery when describe succeeds and `HasDrift == false`, or fix the
  diagram. Related: a *transient* failure during drift correction permanently disables
  self-healing (see §5.4) — distinguish error provenance and let Managed mode retry
  correction with a capped attempts counter.
- **No reconcile timer after a terminally failed Provision** (`scheduleReconcile`
  only on success paths, `s3/driver.go:201,265`) — schedule it on error paths too;
  Reconcile already handles `Error` status.
- **`drivers.ReconcileInterval` is a mutable package-level var** (`state.go:29`) read
  unsynchronized from handlers; make it a constant or atomic.
- **Brittle string matching:** `cmd/praxis-core/main.go:185` detects policy-seed
  conflicts via `strings.Contains(err.Error(), "409")`; prefer typed codes.
  `awserr.HasCode`'s substring fallback (`classify.go:81-85`) can false-positive on
  short codes embedded in wrapped text — add a warning comment.
- **`os.Getenv("PRAXIS_ACCOUNT")` inside a handler** (`command/service.go:193`) isn't
  journaled — a replay after an env change could resolve a different account. Read
  once at startup into `config.Config`.

---

## 4. Performance & scale

### 4.1 Rate limiting can't protect AWS: wrong granularity in three dimensions — HIGH

`internal/infra/ratelimit/limiter.go:39-57`:

1. **Buckets are per-resource-type, not per-AWS-API.** The EC2 API family is split
   across ≥13 independent buckets (`"ec2"`, `"ec2-instance"`, `"vpc"`, `"subnet"`,
   `"elastic-ip"`, `"route-table"`, `"internet-gateway"`, `"nat-gateway"`, `"nacl"`,
   `"vpc-peering"`, `"ebs-volume"`, `"key-pair"`, `"ami"`) — ≈260 rps aggregate
   against EC2's ~20 req/s throttle budget. The package doc's "share a single Limiter
   per AWS service" claim is false in practice (RDS gets it right: 4 drivers share
   `"rds"`).
2. **Not per-account/per-region** — AWS throttles per account-region.
3. **Not shared across processes** — EC2-family calls come from both the network and
   compute packs, and replicas multiply the budget again. `Shared()` is also
   first-caller-wins on rps/burst — silent config divergence.

**Fix:** key buckets by `(awsServiceID, account, region)` (available in `aws.Config`
at client construction); consolidate the EC2 family onto one bucket; divide budgets by
replica count (env var or config) and treat the SDK's adaptive retry mode as the
backstop.

### 4.2 Reconcile herd: fixed 5-minute cadence, no jitter, serialized through one exclusive auth handler — HIGH

`ReconcileIntervalForKind` ignores kind and adds no jitter
(`internal/drivers/state.go:21-38`); all resources provisioned together re-fire in
near-lockstep forever. Every Reconcile first calls `AuthService/<account>/
GetCredentials` — an **exclusive** handler (`internal/core/authservice/service.go:75`)
that serializes the whole fleet per account (and a slow STS refresh blocks everyone).
At ~1,000 resources this is a synchronized 1,000-invocation spike every 5 minutes into
the fragmented rate-limit buckets of §4.1.

**Fix:** (a) deterministic jitter in `scheduleReconcile` —
`interval + fnv(key) % (interval/4)` is replay-safe with no `restate.Run` needed;
(b) real per-kind intervals (IAM doesn't need 5 min); (c) split `GetCredentials` into
a **shared** cached-read handler falling back to the exclusive refresh handler only on
miss/expiry.

### 4.3 Global index objects: O(all-entries) rewrite per status change — MEDIUM

`ResourceIndex`/`DeploymentIndex` (`internal/core/orchestrator/resource_index.go:59-104`,
`index.go:32-57`) deserialize and rewrite the entire entries map on every
`Upsert`/`Remove`, on a single `"global"` key that serializes all writers. Work grows
quadratically with fleet size; submit seeds N entries with N sequential blocking
Requests (`pipeline.go:444-460`).

**Fix:** shard by kind or deployment-key hash prefix, and/or store one entry per state
key within the object; batch submit-time seeding into one slice-taking `Upsert`.

### 4.4 Template engine: full schema recompile per evaluation; shared `cue.Context` across concurrent handlers — MEDIUM

`loadSchemas` runs `load.Instances` + `BuildInstance` on every apply/plan/validate
(`internal/core/template/engine.go:529-557`), and with policies the evaluation runs
`2 + P` times per request. Separately, one `cue.Context` is shared across all handlers
of a stateless (concurrent) Restate service (`command/service.go:88-99`) — CUE's
evaluator has a history of data races under shared contexts; the concurrency-safety
claim in the comment (`engine.go:44-57`) isn't something CUE guarantees.

**Fix:** compile the schema value once in `NewEngine` (schemaDir is immutable); guard
evaluation with a mutex or a `sync.Pool` of contexts; run the integration suite with
`-race` under parallel applies to confirm (ties into §6.2).

### 4.5 Smaller performance items

- **`ListSince` is O(total events) per poll** (`event_store.go:102-127`) — `praxis
  observe` tailing gets progressively slower. Chunks are append-only with monotonic
  sequences: record `firstSequence` per chunk (or exploit fixed `ChunkSize`) and seek
  directly; track the first live chunk after pruning.
- **Delete workflow:** synchronous `PreDelete` (e.g. S3 bucket-emptying, minutes)
  stalls all other delete dispatches (`delete_workflow.go:278-285`); the drain loop
  polls every 2s up to the max provision timeout — ~900 journal entries for an RDS
  teardown (`delete_workflow.go:111-141`). Make `PreDelete` an async invocation
  awaited alongside the delete future; back off the drain poll 2s → 30s.
- **`DeploymentState` is one `"state"` key** — every `UpdateResource` rewrites the
  whole record, O(N²) JSON work per N-resource apply. Fine at current scale; consider
  per-resource state keys past a few hundred resources.
- **Docker builds:** `just up` runs `docker compose build --no-cache`
  (`justfile:21,72`), defeating the Dockerfile's careful layer design, and the
  Dockerfile has no BuildKit cache mounts — every source change re-runs six cold
  `go build`s. Drop `--no-cache` (keep a `just rebuild` escape hatch) and add
  `RUN --mount=type=cache,target=/root/.cache/go-build --mount=type=cache,target=/go/pkg/mod`.
  Extend `.dockerignore` (currently only `.git`, `.env`, `bin/`, `dist/`) with
  `site/`, `docs/`, `charts/`, `tests/`, `examples/` so doc edits don't bust the
  `COPY . .` layer.

---

## 5. Driver layer

(§1.4 waiters and §1.5 duplication are the headline items; the rest:)

### 5.1 Conditions contract implemented by 1 of 51 drivers — MEDIUM

`docs/DRIVERS.md` says every Reconcile must return `Healthy` and `DriftFree`
conditions; only S3 does (`grep -l ConditionHealthy internal/drivers/*/driver.go` →
s3 only). Anything consuming resource conditions sees 50 drivers as permanently
condition-less. Short-term: add a shared
`drivers.BuildConditions(healthy, driftFree bool, reason string)` and wire it into the
five newest drivers (the template for future ones); the harness (§1.5) fixes it
structurally.

### 5.2 Error misclassifications — MEDIUM

- **DynamoDB `LimitExceededException` treated as terminal**
  (`dynamodbtable/driver.go:103-105`, classifier at `aws.go:343-345`). For DynamoDB
  this code means "too many concurrent control-plane ops — retry" (explicitly
  retryable per AWS docs), unlike Secrets Manager/EKS where it's a real quota.
  Creating several tables in one deployment can spuriously hard-fail one. Drop the
  terminal classification for DDB.
- **EKS `ResourceInUseException` terminal on updates**
  (`ekscluster/driver.go:338-340,356-358`). EKS allows one in-flight update; the
  driver issues version + config updates back-to-back, so converging both
  deterministically 409s the second. Terminal is right for Create (name conflict),
  wrong for updates — return it retryable there, or gate `convergeMutableFields` on
  `observed.Status == "ACTIVE"`.
- **Delete-time `DependencyViolation` terminal for SG/VPC/IGW**
  (`sg/driver.go:248-250`, `vpc/driver.go:408-415`, `igw/driver.go:361`). EC2's
  eventual consistency means a correctly-ordered teardown still sees transient
  `DependencyViolation` (Terraform retries these for minutes). Bounded retry inside
  the Run, then terminal.

### 5.3 Immutable-field changes silently ignored in new drivers — MEDIUM

IAM role fails terminally with a clear 409 on immutable change
(`iamrole/driver.go:107-109`), feeding the orchestrator's auto-replace
(`workflow.go:581-595`). EKS and DynamoDB instead silently ignore immutable-field
changes (roleArn/subnets/hashKey never touched by `convergeMutableFields`;
`HasDrift` excludes them) — Provision succeeds, the resource reports Ready, the spec
change is permanently unapplied, and auto-replace can never trigger. **Fix:** in
Provision, when the resource exists and an immutable field differs, return
`TerminalError(..., 409)` matching iamrole/acmcert so `AllowReplace` works uniformly.

### 5.4 Smaller driver items — LOW

- **Pagination gaps:** `lambdalayer/aws.go:89-91` `ListLayerVersions` has no
  `NextMarker` loop (>50 versions ⇒ stale "latest"; the API returns newest-first, so
  taking the first item of the first page suffices); `esm/aws.go:84-93` similarly
  unpaginated (low risk, filtered query). Everything else checked paginates properly.
- **SQS FIFO default:** `sqs/driver.go:547-548` — the `!spec.FifoQueue` guard means
  FIFO queues with omitted `visibilityTimeout` false-drift (desired 0 vs observed 30)
  on direct driver calls; the guard looks unintentional.
- **Policy JSON comparison:** the canonical-JSON round-trip won't equate
  semantic-but-not-syntactic forms (`"Action": "s3:*"` vs `["s3:*"]`). Safe today,
  but extract the five copies of `policiesEqual` into a shared `policyjson` package
  with IAM-shape normalization (lift scalars to arrays) before it bites.
- **S3 encryption drift** only checked when `desired.Encryption.Enabled`
  (`s3/drift.go:34-38`) — deliberate-looking (AWS encrypts by default now) but
  deserves a comment.

---

## 6. Testing & CI

The suite quality is genuinely high (see §8). Findings:

### 6.1 One Restate container per test function — HIGH (cost)

386 integration test functions each boot a fresh Restate container + deployment
registration (`setupDriverEventingEnv` → `restatetest.Start`,
`drift_event_helpers_test.go:21`). At 4–6s per boot that's 25–40 minutes of pure
container churn — the reason the CI timeout grew to 40m and the harness needed
churn-retry logic (`restatetest.go:127`). Eleven *unit* test files also boot per-test
containers, forcing `just test -p 1`.

**Fix:** one shared Restate container per test package (`TestMain` or `sync.Once`)
with the package's full service set bound; tests already use unique object keys
(`uniqueDynamoDBTableName`, `uniqueCidr`). Keep per-test containers only for
`crashresume_test.go` and `faultinjection_test.go`, which mutate container state.
Expect 5–10× wall-clock reduction.

### 6.2 No coverage measurement; integration never runs `-race` — MEDIUM

Nothing in ci.yml or the justfile measures coverage, so there's no visibility into
which of the 51 drivers' drift/error branches are exercised. And `test-integration`
(justfile:492) lacks `-race` while the concurrency-heavy orchestrator/event paths only
get realistic concurrent load in integration (also the confirmation test for §4.4's
CUE-context race).

**Fix:** add `-coverprofile -covermode=atomic` to `just test` (integration can use
`-coverpkg=./internal/...` since drivers run in-process), upload as artifact, add a
cheap no-regression threshold; add `-race` to integration (weekly scheduled job if the
2× slowdown breaks the 50-min budget).

### 6.3 Integration tree invisible to golangci-lint — MEDIUM

`.golangci.yml` has no `run.build-tags`, so the ~15k LOC under `//go:build
integration` is never linted. **Fix:**

```yaml
run:
  build-tags: [integration]
```

Bump the lint timeout to 10m. Also worth adding: `testifylint` (heavy testify usage;
catches require-vs-assert misuse and arg-order bugs), `thelper`, `tparallel`.

### 6.4 Availability-skips can silently green-out whole suites — MEDIUM

Of 34 `t.Skip`s, the documented-Moto-limitation class is fine policy, but
*availability* skips ("Moto IAM service is not enabled", `iamrole:104`,
`iamuser:41-61`; "SQS API unavailable", `sqs_helpers:40`) mean a Moto image or seed
regression silently turns entire driver suites green in CI. **Fix:** set
`PRAXIS_REQUIRE_MOTO=1` in CI and have those helpers `t.Fatal` instead of `t.Skip`
when the probe fails.

### 6.5 `just ci` ≠ CI — MEDIUM

`just ci` (justfile:796) omits `fmt-check`, `build`, the CUE schema-drift check, and
helm lint — all of which gate CI. A dev whose `just ci` passes can still fail CI.
**Fix:** `ci: lint fmt-check test build check-schema-drift test-integration`, with a
single `check-schema-drift` recipe that CI also calls.

### 6.6 Missing test genres worth adding — LOW

No fuzz, property, or golden tests anywhere. In priority order: (1) golden tests for
plan/diff output (exactly the kind of output that silently reorders); (2) a property
test shared across drivers — `SpecFromObserved(Observed(spec))` round-trip ⇒
`HasDrift == false` — which mechanically generalizes the vacuous-test class already
fixed once, across all 51 drivers; (3) fuzz targets for spec validation and the
template `${...}` resolver (user-controlled parse surfaces).

### 6.7 Small CI items — LOW

- No `concurrency` group — superseded pushes run the full 40-min integration job to
  completion. Add `concurrency: { group: ci-${{ github.ref }}, cancel-in-progress: true }`.
- golangci-lint compiled from source every run (ci.yml:32) — use
  `golangci/golangci-lint-action` with a pinned version (saves 1–3 min).
- Test-hygiene time bombs for the day `t.Parallel` arrives: non-atomic package-level
  `cidrCounter` (vpc_driver_test.go:42), mutated global `ReconcileInterval`
  (state_test.go:13-14), `UnixNano()%100000` unique names (collision window). Fix
  cheaply now: atomic counter, `t.Name()`-derived names.
- `pollDriftEventTypes` returns partial results on timeout instead of `t.Fatal`
  (drift_event_helpers_test.go:84-86) — failures read as "assert.Contains failed"
  instead of "timed out waiting for drift events".

---

## 7. Developer experience & docs

### 7.1 Get Started is broken: the README drives everything through files that don't exist — HIGH

`README.md:168-190,243` uses `webapp.cue` / `vars.json` — neither exists anywhere in
the repo. A new user hits a wall immediately after `just up`. Also: the snippets call
`praxis ...` but `just build-cli` puts the binary at `bin/praxis` with nothing on
PATH; and `jq` is required by `just up`'s final registration step
(justfile:119-149) but isn't in the README prerequisites.

**Fix:** use a real example (`examples/ec2/dev-instance.cue`, which is exactly what
`examples/README.md:20-28` does) or ship `examples/stacks/webapp.cue`; use
`./bin/praxis` in snippets; add jq (and `cue`, golangci-lint for dev work) to
prerequisites.

### 7.2 A shipped example is broken because the template engine silently skips unknown kinds — HIGH

`examples/acm/https-stack.cue:57-68` uses `kind: "DNSRecord"` — no such schema (the
real kind is `Route53Record`), plus a wrong field name and a nonexistent `tags` block.
It's advertised as ready-to-use (`README.md:321`). Root cause: when `#<kind>` doesn't
exist, the engine **silently skips schema unification**
(`internal/core/template/engine.go:457-462`) — a typo'd kind gets zero validation and
only fails at deploy time.

**Fix:** (1) fix the example; (2) make the engine return a `TemplateError` for unknown
kinds when schemas are loaded; (3) add `just validate-examples` + a CI step running
every example (with its vars) through the template engine — this would have caught the
rot. The CI already has exactly this gate pattern for event schemas.

### 7.3 Registration race + silent failure in `just up` — MEDIUM

`wait-stack` (justfile:38-67) polls Moto and the Restate admin port but never the
`praxis-*` services' :9080 endpoints; `register` then pipes `curl -s` (no `-f`, no
pipefail) through jq — if a service isn't listening or Restate returns an error JSON,
the recipe prints the error body and still echoes "✓ registered". Compose defines no
healthchecks for the `praxis-*` services. **Fix:** `curl -fsS --fail-with-body` so the
recipe aborts on non-2xx; add healthchecks (or port polls) for the service containers.

### 7.4 The justfile is a 1,120-line copy-paste dumping ground — MEDIUM

~55 per-driver `test-*` recipes differ only in package path; ~30 integration recipes
duplicate the same 10-line heartbeat loop verbatim; timeouts vary 3m/5m/10m/15m
arbitrarily; indentation is mixed tabs/spaces. The predictable consequence already
happened: `release-service-preflight` (justfile:1024-1028) is missing all five new
drivers (dynamodbtable, ekscluster, ecscluster, kmskey, secret) — a per-service
release would ship them **untested**, defeating the recipe's own "refusing to release
untested" guard. **Fix:** two parameterized recipes — `test-driver PKG:` and
`_integration PATTERN TIMEOUT="10m":` with the heartbeat written once (~600 lines
deleted) — and backfill the five drivers into the preflight sets now.

### 7.5 Driver inventory is hand-maintained in four places and has already diverged — MEDIUM

`README.md:307-313`, `docs/DRIVER_ROADMAP.md` (canonical, 51/51 correct),
`docs/DRIVERS.md:449-455` (stale: missing DynamoDBTable, EKSCluster/ECSCluster,
KMSKey/SecretsManagerSecret), `AGENTS.md:22` ("45 resource drivers" — actual 51,
propagated into every agent session). Compose header comments (`docker-compose.yaml:
10-14`) still say "(future: DynamoDB)". **Fix:** fix the stale copies now; make
DRIVER_ROADMAP.md canonical and point the others at it.

### 7.6 openapi.yaml is hand-written and drifting — MEDIUM

`api/openapi.yaml` covers 11 of 20 `PraxisCommandService` handlers — missing
ApplySavedPlan, Approve, Reject, RollbackTo, DeleteTemplate, and the policy CRUD,
even though `docs/API.md:81,103` ships curl examples for some of them. **Fix:** add
the missing operations and a cheap CI drift gate (script comparing spec paths against
registered handler names) — the event-schema drift gate is the template.

### 7.7 CLI ergonomics — MEDIUM

- **Connection-refused is the most common local failure and gets a raw Go error**
  (`internal/cli/client.go:168,191,292`): `dial tcp ... connection refused`, no hint.
  Detect it in `HandleError` and print "cannot reach Restate ingress at <endpoint> —
  is the stack running? try `just up` (or set PRAXIS_RESTATE_ENDPOINT)".
- **Timeout paths hardcode `os.Exit(2)`** (`deploy.go:443`, `delete.go:173`,
  `observe.go:86`), bypassing the otherwise fully centralized exit mapping; the
  defined `ExitTimeout` constant is never referenced. Return a sentinel error mapped
  in `ExitCodeForError`.
- **Version is only stamped on release builds** — everyday `just build` and all
  Docker images report `praxis dev (built unknown)`. Add
  `-ldflags "-X ...version=$(git describe --tags --always --dirty)"` to the common
  build recipe and Dockerfile.
- **`praxis fmt` ignores `-o json`**, contradicting AGENTS.md's "every command
  supports `-o json`" (`internal/cli/fmt.go:33-69`). Fix fmt or scope the claim.
- Smaller: document the (working) cobra `completion` subcommand; add
  `ValidArgsFunction` kind completion from the existing kind maps; add
  `PRAXIS_POLICY_DIR` to `.env.example`.

### 7.8 Contributor experience — MEDIUM

No CONTRIBUTING.md, no issue/PR templates, no CHANGELOG (notes live only in annotated
tags). The excellent `docs/DEVELOPERS.md` deserves a CONTRIBUTING.md that links to it,
plus one issue template and a PR template.

---

## 8. What's working well

Credit where due — these were verified, not assumed:

- **Replay-safety discipline in the orchestrator** is unusually good: journaled clock
  helper, deterministic FNV jitter, sorted iteration before every journaled per-item
  call, comments citing the exact failure mode. The driver `time.Now()` gap (§3.2) is
  the exception, not the rule.
- **Single-atomic-state-key driver pattern** (`drivers/state.go`) — an elegant answer
  to torn-state bugs. TOCTOU on submit correctly closed inside the exclusive handler.
- **Honest threat model** (`docs/AUTH.md`) that names the ingress as the trust
  boundary instead of hiding it; saved-plan HMAC with constant-time compare that
  rejects unsigned plans when a key is set; virtual-object key-injection defense;
  distroless nonroot images; compose ports bound to 127.0.0.1; no credentials in logs.
- **Drift detection is genuinely thought through:** field-by-field comparison (one
  justified `DeepEqual` in the codebase), AWS-default normalization, order-insensitive
  tags, semantic IAM policy comparison, sorted-set SG rules, LateInit.
- **Crash-resume and fault-injection test harnesses** that actually verify the
  product's core durability claim, with replay-safe in-`Run` counters; all waits are
  deadline-bounded polls; every skip carries a documented reason.
- **CI = the same `just` recipes developers run; releases are integration-gated**,
  with checksummed artifacts, ghcr images, and an OCI Helm chart from one tag-triggered
  flow; an event-schema drift gate already exists as the pattern to copy for examples
  and openapi.
- **Docs accuracy is above average:** CLI exit codes match code byte-for-byte,
  DRIVER_ROADMAP is 51/51 correct, INDEX.md has zero dead links, skills manifest is
  10/10 accurate.

---

## 9. Prioritized action plan

Status legend: ✅ landed on this branch · ◑ partially landed (see the per-guide
"Implementation status" notes in Appendix A) · ☐ not started.

| # | Action | Findings | Effort | Status |
|---|--------|----------|--------|--------|
| 1 | Mask sensitive fields in create-path plan diffs, execution plans, and `GetInputs` | §1.1 | S–M | ✅ (plan diffs masked via descriptor SensitiveFields; `GetInputs` masked at the CLI boundary with a registry guard test — see A9; execution-plan `Spec` masking still TODO — see A2) |
| 2 | Fix `decodeRetryableInvocationError` to trust the 429/425 code without the "retryable" substring | §1.2 | S | ✅ |
| 3 | Add EKS/DynamoDB `WaitReady` adapters; convert ec2/natgw/lambda blocking waits to bounded durable polls | §1.4 | M | ◑ (EKS/DynamoDB durable waits with ACTIVE-gated Provision **and** Reconcile, headroom budgets, no-op fast path; alb/nlb bounded; ec2/natgw/lambda deferred — see A3) |
| 4 | Cache CUE schema compilation (events + template engine); decouple event emission from workflow success | §1.3, §4.4 | M | ◑ (event-schema cache done; emit decoupling done with load-bearing carve-out for ready + terminal events — see A4; template-engine schema cache/context is §4.4, not done) |
| 5 | Secrets Manager: recovery window by default; hash-compare instead of storing secret values | §2.2, §2.3 | S | ◑ (recovery window + `forceDelete` + restore-on-reprovision + idempotent delete done — see A5; observed-value hashing deferred — see A9) |
| 6 | Fix README Get Started (phantom webapp.cue), broken ACM example, and make the engine reject unknown kinds | §7.1, §7.2 | S | ✅ (plus schema-load-failure diagnostics and an EXTENDING.md accuracy fix — see A6) |
| 7 | Shared Restate container per test package (5–10× integration wall-clock) | §6.1 | M | ☐ |
| 8 | Sorted multi-future `WaitFirst` to fix head-of-line blocking | §3.1 | M | ☐ |
| 9 | Rate-limit by (service, account, region); reconcile jitter + shared credential reads | §4.1, §4.2 | M | ◑ (reconcile jitter done, scaled into the full band, all 51 sites — A7; rate-limit re-keying + shared credential reads not done) |
| 10 | Generic driver harness; migrate the five newest drivers first | §1.5, §5.1, §5.3, §3.2 | L | ☐ |
| 11 | Supply chain: SHA-pin actions, govulncheck, dependabot, versioned image tags | §2.5 | S | ☐ |
| 12 | CI hygiene: coverage, `-race` integration, lint build-tags, `just ci` parity, concurrency group | §6.2–§6.5 | S–M | ✅ (coverage, lint build-tags, `just ci` parity, concurrency group, golangci-lint-action; `-race` on integration left off to protect the 50-min budget) |
| 13 | Justfile dedup + backfill new drivers into release preflight | §7.4 | S | ◑ (release-preflight backfilled for all 5 new drivers; recipe dedup not done) |
| 14 | NetworkPolicy template + CLI API-key support; sink header redaction + SSRF guard | §2.1, §2.6 | M | ☐ |
| 15 | Rollback-vs-retention fix; approval-gate error provenance; sink delivery docs-vs-code | §3.3–§3.5 | M | ☐ |

Items 1–6 are the "before anyone else touches this in anger" set: two secret-handling
gaps, two correctness holes in the product's core promise (retry and readiness), and
the two things every new user hits in their first ten minutes.

**Landed on this branch (A1–A9 from Appendix A):** items 1, 2, 6, 12 in full; 3, 4,
5, 9, 13 in part (deferrals documented inline in each Appendix A guide's
"Implementation status" note). All changes compile, `gofmt`-clean, `go vet`-clean, and
`golangci-lint run ./...` reports 0 issues with the integration tree now included;
new unit tests cover the retry decode, plan-diff masking, unknown-kind rejection, and
reconcile jitter. Nothing is committed.

**Post-review fix round:** an adversarial multi-agent review of the branch (8
finder angles + verification) surfaced 10 findings in the A1–A9 implementation
itself; all 10 were then fixed and re-verified (full unit suites with `-race`,
zero lint issues, and live Moto integration runs for the secret/EKS/DynamoDB
drivers). The headline corrections: the secret recovery-window default gained a
restore-on-reprovision path (probed live against Moto) instead of locking names
for 7 days; ready/terminal event emission stays error-propagating (best-effort
would have silently corrupted rollback plans and hung `praxis observe`);
GetInputs masking moved from the drivers to the CLI boundary with a
registry-sync guard test, restoring the observe-before-act fast path; the
jitter hash is scaled (the modulo version silently capped jitter at ~4.3s) and
the two `shared.`-aliased call sites the sweep missed were converted; Reconcile
drift correction is ACTIVE-gated like Provision; wait budgets got headroom;
"disappeared" waiter errors are 404 per convention; and `*.out` is
docker-ignored. Details are in each guide's status note above.

---

## Appendix A — Implementation guides for automated fixes

This appendix makes the highest-priority fixes executable by a less capable model (or
a rushed human) without re-deriving the analysis. Each guide lists the exact files,
steps, code shapes taken from this repo, verification commands, and what is *out of
scope*. General rules for any automated fixer working in this repo:

- Error classification MUST happen inside `restate.Run()` callbacks: terminal errors
  wrapped with `restate.TerminalError()`, transient ones returned bare
  (see `AGENTS.md` and `docs/ERRORS.md`).
- Never introduce `time.Now()`, `rand`, or map-order-dependent logic outside
  `restate.Run` in handler code — it breaks journal replay.
- After any change: `just lint && gofmt -l . && go test ./internal/... ./pkg/...`
  (unit tests need Docker running). Run the matching integration test with
  `go test -tags integration ./tests/integration/ -run <TestName> -timeout 10m`.
- Do not "improve" adjacent code. One finding per branch/commit.

### A.0 Triage: which items a smaller model can safely do

| Suitable for a smaller model (guides below) | Needs a strong model or human (do NOT hand to a small model) |
|---|---|
| A1 retryable-error decode (§1.2) | §1.5 generic driver harness — large API-design refactor |
| A2 sensitive-field masking (§1.1) | §3.1 multi-future `WaitFirst` — journal-compatibility risk; an error here corrupts replay for in-flight deployments |
| A3 EKS/DynamoDB durable waiters (§1.4) | §1.3 EventBus removal (the *cache* part is A4; the topology change is not) |
| A4 CUE schema caching + emit decoupling (§1.3) | §4.1 rate-limiter re-keying — needs a call-site sweep across 51 drivers with judgment per service family |
| A5 Secrets Manager recovery window (§2.3) | §4.3 index sharding — data-migration design for existing state |
| A6 template-engine unknown-kind error + example/README fixes (§7.1, §7.2) | §3.3 rollback-vs-retention — semantic redesign of the rollback plan source |
| A7 reconcile jitter (§4.2a) | §6.1 shared test containers — safe in principle, but touches 70 test files; do it package-by-package with a strong reviewer |
| A8 CI/justfile hygiene (§6.2, §6.3, §6.5, §6.7, §7.4) | §3.5 approval awakeable protocol — requires tracing the command-service resolve/reject flow end to end |
| A9 secret `GetInputs` blanking + observed-hash (§1.1, §2.2) | |

### A1 — Fix the dead retry path (§1.2)

**File:** `internal/core/provider/helpers.go:66-86`.

Replace `decodeRetryableInvocationError` with:

```go
func decodeRetryableInvocationError(err error) error {
	if err == nil {
		return nil
	}
	if retryable, ok := types.IsRetryable(err); ok {
		return retryable
	}
	// Terminal errors carrying a throttle/limit status code are retryable at
	// the orchestrator level even though the driver invocation itself is done.
	// 425 = TooEarly (Restate), 429 = throttled/limit, 503 = service unavailable.
	switch restate.ErrorCode(err) {
	case 425, 429, 503:
		message := strings.TrimSpace(err.Error())
		if message == "" {
			message = "retryable resource operation failed"
		}
		return types.NewRetryableError(errors.New(message))
	}
	return nil
}
```

The only change is deleting the `strings.Contains(..., "retryable")` gate and adding
503. **Verify:** add a unit test in the same package asserting that
`restate.TerminalError(errors.New("SubscriptionLimitExceeded: ..."), 429)` decodes to
a retryable error, and that a 409 does not. Then confirm the orchestrator backoff
tests still pass: `go test ./internal/core/orchestrator/...`. **Out of scope:** do not
change how drivers classify errors; do not touch `backoff.go`.

### A2 — Mask sensitive fields in create-path plan diffs (§1.1, part 1)

**Files:** `internal/core/provider/generic.go`, `internal/core/provider/helpers.go`,
plus the four descriptor definitions.

1. Add to `GenericDescriptor[S, O, Obs]` (`generic.go:36`, after `DiffFields`):

```go
	// SensitiveFields lists spec paths (dot notation, e.g. "spec.secretString")
	// whose values must never appear in plan output. Matching diff entries are
	// masked with "(sensitive)". Prefix match: "spec.value" also masks
	// "spec.value.anything".
	SensitiveFields []string
```

2. In `planCreate` (`generic.go:244-251`), after `createFieldDiffsFromSpec` returns,
   mask before returning:

```go
	diffs = maskSensitiveFieldDiffs(diffs, a.desc.SensitiveFields)
```

   and add to `helpers.go`:

```go
func maskSensitiveFieldDiffs(diffs []types.FieldDiff, sensitive []string) []types.FieldDiff {
	if len(sensitive) == 0 {
		return diffs
	}
	for i, d := range diffs {
		for _, s := range sensitive {
			if d.Path == s || strings.HasPrefix(d.Path, s+".") {
				if d.New != nil && d.New != "" {
					diffs[i].New = "(sensitive)"
				}
				if d.Old != nil && d.Old != "" {
					diffs[i].Old = "(sensitive)"
				}
			}
		}
	}
	return diffs
}
```

   (Check `types.FieldDiff`'s actual field names — `Old`/`New` vs
   `Before`/`After` — in `pkg/types` before writing; use whatever the update-path
   maskers in `internal/drivers/secret/drift.go:50` use, and the same
   `"(sensitive)"` literal.)

3. Set `SensitiveFields` in these descriptors (each lives in
   `internal/core/provider/<kind>_adapter.go` or the shared descriptor file — locate
   with `grep -rn "Kind:" internal/core/provider/ | grep -i <kind>`):
   - SecretsManagerSecret: `{"spec.secretString"}`
   - SSMParameter: `{"spec.value"}`
   - RDSInstance: `{"spec.masterUserPassword"}`
   - AuroraCluster: `{"spec.masterUserPassword"}`

4. Apply the same masking to the expression-resource fallback:
   `internal/core/command/plan_diff.go:184-217` (`FieldDiffsFromJSON`) — thread the
   compiled template's `Sensitive` path set
   (`internal/core/command/pipeline.go:253-268`, field at `:93-95`) through
   `computeResourceDiffs` and mask any diff whose path is in it, reusing the
   prefix-match rule above. The masking helper for rendered output already exists
   at `pipeline.go:768` (`maskSensitiveValues`) — mirror its path semantics.

**Verify:** integration test — register a template containing a
`SecretsManagerSecret` with `secretString: "hunter2"`, call plan, and assert the
string `hunter2` does not appear anywhere in the `PlanResponse` JSON
(`tests/integration/secret_driver_test.go` has the setup pattern). **Out of scope:**
`ExecutionPlan.Resources[].Spec` masking (needs a deploy-time re-resolve design —
leave a TODO referencing §1.1 fix 2), and `GetInputs` (that's A9).

### A3 — Durable, bounded readiness waits (§1.4)

**Pattern (already in-repo):** `internal/drivers/alb/driver.go:456` `waitForActive` —
one Describe per `restate.Run`, `restate.Sleep(ctx, 10*time.Second)` between
attempts. It is durable but unbounded. The target shape, bounded:

```go
const (
	readyPollInterval = 15 * time.Second
	readyMaxAttempts  = 60 // ~15 minutes
)

func (d *EKSClusterDriver) waitForActive(ctx restate.ObjectContext, api EKSClusterAPI, name string) (ObservedState, error) {
	for attempt := 0; attempt < readyMaxAttempts; attempt++ {
		observed, found, err := restate.Run(ctx, func(rc restate.RunContext) (observeResult, error) {
			// reuse the driver's existing observe function
		})
		if err != nil {
			return ObservedState{}, err
		}
		if !found {
			return ObservedState{}, restate.TerminalError(fmt.Errorf("cluster %s disappeared while waiting for ACTIVE", name), 500)
		}
		switch observed.Status {
		case "ACTIVE":
			return observed, nil
		case "FAILED":
			return ObservedState{}, restate.TerminalError(fmt.Errorf("cluster %s entered FAILED state", name), 500)
		}
		if err := restate.Sleep(ctx, readyPollInterval); err != nil {
			return ObservedState{}, err
		}
	}
	return ObservedState{}, restate.TerminalError(fmt.Errorf("cluster %s not ACTIVE after %s", name, time.Duration(readyMaxAttempts)*readyPollInterval), 500)
}
```

Apply, one PR each:

1. **ekscluster** — call `waitForActive` in Provision between the create-observe and
   the `state.Status = types.StatusReady` write (`driver.go:125-135`), storing the
   final observed state. Also gate `convergeMutableFields` on
   `observed.Status == "ACTIVE"` (fixes half of §5.2's EKS 409s).
2. **dynamodbtable** — same, status field is `TableStatus`, target `"ACTIVE"`.
3. **alb / nlb** — add the `attempt < maxAttempts` bound and the timeout
   terminal-error to the existing loops; change nothing else.
4. **ec2** (`aws.go:208-213`), **natgw** (`aws.go:117-131`), **lambda**
   (`aws.go:284-303`) — move the wait out of the single `restate.Run`: delete the
   blocking SDK-waiter/sleep-loop from the `aws.go` function, and add a
   driver-level `waitForX` loop (shape above) at the call site in `driver.go`.
   Each `restate.Run` must contain exactly one Describe call and no sleeps.

> **Implementation status (this branch):** items 1–3 are done — EKS and DynamoDB
> wait durably for ACTIVE before reporting Ready, and the alb/nlb loops are
> bounded. Post-review hardening: the ACTIVE gate is applied in **both**
> Provision and Reconcile drift correction (a non-ACTIVE resource defers
> correction to the next cycle instead of wedging in Error via terminal
> `ResourceInUseException`); the wait reuses the initial observe and skips
> entirely when the resource is already ACTIVE (no extra describes on the no-op
> fast path); budgets carry real headroom (EKS 100×15s ≈ 25 min vs the 10–15 min
> typical create, DDB 90×10s = 15 min to survive GSI backfills); and the
> "disappeared while waiting" case is coded 404 per docs/ERRORS.md, not 500.
> Item 4 (ec2/natgw/lambda) is intentionally **deferred**: those SDK waiters are
> already time-bounded (5/10/2 min), so the residual issue is durability/
> lock-hold rather than the correctness or infinite-loop bugs fixed above, and
> the delete-path conversion (natgw `WaitUntilDeleted`) carries regression risk
> that Moto's instant state transitions cannot exercise in tests. It should land
> as its own change with timing-aware integration coverage.

**Caveats:** Moto transitions resources to ACTIVE/running instantly or near-instantly,
so integration tests mostly exercise the zero-iteration path — that's fine; assert
provisioning still succeeds. Do NOT implement adapter-side `WaitReady` for EKS/DDB
(stale-state trap, see §1.4). **Verify:**
`go test -tags integration ./tests/integration/ -run 'TestEKS|TestDynamoDB' -timeout 15m`
and the driver unit tests (mock API pattern is in each driver's `aws_test.go` /
`driver_test.go`).

### A4 — CUE schema caching + event-emit decoupling (§1.3 parts 1 and 3)

**Part 1 — cache compiled event schemas.** File:
`internal/core/cuevalidate/validate.go:63-90`. Today `DecodeFile` does
`os.ReadFile` + `cuecontext.New()` + `CompileBytes` per call. Add a package-level
cache:

```go
var schemaCache sync.Map // key: path + "|" + definition → cachedSchema

type cachedSchema struct {
	mu  sync.Mutex // CUE contexts are not safe for concurrent use; serialize per schema
	val cue.Value
}
```

On lookup miss: read + compile once, store. On hit: lock `mu`, unify/validate the
incoming event against `val`, unlock. The schema dir is immutable at runtime
(mounted read-only in compose), so no invalidation is needed. Important: because a
`cue.Context` must not be shared across goroutines without synchronization, keep the
per-entry mutex — validation is still ~1000× cheaper than a fresh context + compile.

**Part 2 — stop failing workflows on emit errors.** File:
`internal/core/orchestrator/workflow.go:753-755` (and any other `return err` sites
found by `grep -n "EmitDeploymentCloudEvent\|EmitCloudEvent" internal/core/orchestrator/workflow.go delete_workflow.go`).
Change each `if err := ...Emit...; err != nil { return ... }` in workflow bodies to:

```go
	if err := r.EmitDeploymentCloudEvent(ctx, ...); err != nil {
		ctx.Log().Error("event emission failed; continuing", "error", err)
	}
```

Only in *workflow* code paths — leave command-service emit errors as-is (callers may
rely on them). **Verify:** `go test ./internal/core/... ` plus
`go test -tags integration ./tests/integration/ -run TestCore -timeout 15m`; the
event-schema drift check (`just generate-event-schemas && git diff --exit-code`)
must stay clean. **Out of scope:** removing the EventBus hop (topology change,
strong-model work).

> **Implementation status (this branch):** both parts done, with one critical
> carve-out discovered in review: **not every workflow event may be
> best-effort.** Two event classes are load-bearing — `resource.ready` events
> (RollbackPlan in event_store.go is built exclusively from them; a silently
> dropped one orphans the resource in every future rollback) and deployment
> terminal events (`praxis observe` exits only when it reads one). Those sites
> keep the error-returning `EmitDeploymentCloudEvent`, each with a comment
> saying why; the ~22 informational sites use the BestEffort wrappers, whose doc
> comment (runtime.go) names the load-bearing exceptions. The durable fix for
> the ready-event dependency is §3.3 (derive RollbackPlan from DeploymentState),
> still open.

### A5 — Secrets Manager recovery window (§2.3)

**Files:** `internal/drivers/secret/aws.go:160-172`, `internal/drivers/secret/types.go`,
the Secrets Manager CUE schema (find with `grep -rln "secretString" schemas/`), and
`internal/drivers/secret/aws.go:210-212`.

1. Add to `SecretsManagerSecretSpec` in `types.go`:
   `ForceDelete bool \`json:"forceDelete,omitempty"\`` — and mirror it in the CUE
   schema as an optional `forceDelete?: bool | *false`. Run
   `just generate-event-schemas` if schemas feed generation; check
   `git diff schemas/`.
2. Change `DeleteSecret` to accept the flag:

```go
func (r *realSecretsManagerSecretAPI) DeleteSecret(ctx context.Context, name string, force bool) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &secretsmanager.DeleteSecretInput{SecretId: aws.String(name)}
	if force {
		input.ForceDeleteWithoutRecovery = aws.Bool(true)
	} else {
		input.RecoveryWindowInDays = aws.Int64(7)
	}
	_, err := r.client.DeleteSecret(ctx, input)
	return err
}
```

   Update the interface, mock, and the Delete handler call site
   (`driver.go` — pass `state.Desired.ForceDelete`). Update the doc comment.
3. Recreate-after-delete: in the create path's error classification, treat
   `InvalidRequestException` whose message contains "scheduled for deletion" as
   retryable (return it bare, not via `IsInvalidParam` → terminal). Add a narrow
   check *before* the `IsInvalidParam` branch.

> **Implementation status (this branch):** fully done, with a better mechanism
> than step 3's retry suggestion. The recovery-window default and `forceDelete`
> opt-in (spec field + CUE schema) are in. Recreate-after-delete is handled by
> **restoring** rather than retrying: `DescribeSecret` detects a scheduled
> deletion structurally (`DeletedDate != nil` → `ObservedState.
> ScheduledForDeletion`, skipping the value read that would throw
> `InvalidRequestException`), and `convergeMutableFields` calls `RestoreSecret`
> first when set — so delete-then-redeploy, force-replace, and Reconcile-time
> external soft-delete all converge declaratively instead of failing or waiting
> out the window. Delete is idempotent on an already-scheduled secret
> (`IsScheduledForDeletion` matches AWS "scheduled/marked for deletion" and
> Moto's "currently marked deleted" wording — probed live against Moto).
> Integration coverage: soft-delete semantics, `forceDelete: true` immediate
> deletion, and restore-on-reprovision (`tests/integration/secret_driver_test.go`).

### A6 — Template engine unknown-kind error + broken example + README (§7.1, §7.2)

1. **Engine** — `internal/core/template/engine.go:457-462`. Current code silently
   skips unification when the kind has no schema. Change to:

```go
		if schemaVal != nil {
			schemaDef := schemaVal.LookupPath(cue.ParsePath("#" + kind))
			if !schemaDef.Exists() {
				return nil, &TemplateError{ /* match the struct's existing fields; message: */
					// fmt.Sprintf("resources.%s: unknown kind %q — no schema #%s found", name, kind, kind)
				}
			}
			resVal = resVal.Unify(schemaDef)
		}
```

   Inspect `TemplateError`'s definition in this package first and construct it the
   way neighboring validation errors do (there is one ~20 lines below at the
   `Validate` error branch — copy that shape, including the `resources.<name>` path
   convention). Only error when `schemaVal != nil` (schema-less evaluation, used by
   some tests, must keep working).
2. **Example** — `examples/acm/https-stack.cue`: change `kind: "DNSRecord"` →
   `kind: "Route53Record"` (three occurrences near lines 57, 65, 68), rename field
   `records:` → `resourceRecords:`, delete the `tags:` block (schema:
   `schemas/aws/route53/record.cue`). Validate by running the engine's own test
   helper or `cue vet` against the schema; if the repo has a `praxis fmt`/validate
   path, `./bin/praxis fmt examples/acm/https-stack.cue`.
3. **README** — `README.md:168-190,243`: replace `webapp.cue` flows with
   `examples/ec2/dev-instance.cue` exactly as `examples/README.md:20-28` does, and
   change bare `praxis` invocations to `./bin/praxis`. Add `jq` to the prerequisites
   list near `README.md:145`.
4. **Regression gate** — add to the justfile:

```make
# Validate every example template against the schemas.
validate-examples:
    for f in $(find examples -name '*.cue' -not -name '*vars*'); do \
        ./bin/praxis fmt "$f" >/dev/null || exit 1; \
    done
```

   …but first check whether `praxis fmt` actually performs schema validation; if it
   only formats, wire the engine's `EvaluateBytes` via a tiny
   `cmd/praxis validate`-style path or a Go test in
   `internal/core/template` that walks `examples/`. A Go test
   (`TestExamplesValidate`) is the more reliable option and needs no binary.

**Verify:** new engine unit test: template with `kind: "Nonexistent"` must fail with
a message containing `unknown kind`; the fixed example must pass the new
examples-validation test.

> **Implementation status (this branch):** done, with two post-review
> amendments. (1) The review worried the unknown-kind rejection broke
> docs/EXTENDING.md's "schema-less external kinds work today" claim — tracing
> the code showed that claim was already false: the Apply pipeline
> (`pipeline.go` → `providers.Get`) and the deploy workflow both hard-reject
> kinds without a registry adapter, so the eval-time check breaks no working
> flow; EXTENDING.md was corrected to match reality (direct Restate invocation
> works without core changes; templates/deployments require the adapter +
> schema). (2) `loadSchemas` used to silently skip schema packages that fail to
> load/build, so a broken schema file would misdiagnose as "unknown kind" —
> skips are now collected and included in the ErrUnknownKind detail. The
> examples-validation gate turned out to already exist
> (`TestEngine_Evaluate_AllExamples`, run by `just test`) and it caught the
> broken ACM example the moment the engine check landed.

### A7 — Reconcile jitter (§4.2a)

**File:** `internal/drivers/state.go` (helper), plus each driver's
`scheduleReconcile` — but do NOT edit 51 files: change the shared entry point.
`scheduleReconcile` implementations all call
`drivers.ReconcileIntervalForKind(ServiceName)` with a `restate.WithDelay` (see
`internal/drivers/s3/driver.go:544-552`). Add a keyed variant and switch call sites
mechanically:

```go
// ReconcileDelayFor returns the reconcile delay for a kind/key pair with
// deterministic per-key jitter (0–25% of the interval). Deterministic (FNV of
// the key) so it is identical on journal replay — no restate.Run needed.
func ReconcileDelayFor(kind, key string) time.Duration {
	interval := ReconcileIntervalForKind(kind)
	h := fnv.New32a()
	h.Write([]byte(key))
	// SCALE the hash into the band — do NOT take it modulo the band. A uint32
	// interpreted as nanoseconds tops out at ~4.3s, so a modulo against any
	// band wider than that never wraps and jitter silently caps at ~4.3s.
	permille := int64(h.Sum32() % 1000)
	jitter := time.Duration(int64(interval/4) * permille / 1000)
	return interval + jitter
}
```

Then in every `scheduleReconcile`:
`drivers.ReconcileIntervalForKind(ServiceName)` →
`drivers.ReconcileDelayFor(ServiceName, restate.Key(ctx))`. This is a safe
mechanical sed across `internal/drivers/*/driver.go`; the deterministic-input rule
is satisfied because `restate.Key(ctx)` is stable — but grep for ALL spellings
first: two drivers (sqs, sqspolicy) import the package as `shared` and used bare
`shared.ReconcileInterval`, which a sweep keyed on one spelling misses.
**Verify:** unit tests asserting `ReconcileDelayFor` is deterministic per key,
≥ interval, < 1.25×interval, AND that across many keys the jitter actually fills
the band (a max-observed-jitter assertion — the modulo bug passes the range
checks but never exceeds ~4.3s); `go test ./internal/drivers/...`. Note
`internal/drivers/state_test.go:13-14` mutates `ReconcileInterval` globally —
keep the new helper reading it the same way so those tests still pass.
**Out of scope:** per-kind intervals and the AuthService shared-read split
(§4.2b/c).

> **Implementation status (this branch):** done as above, including the scaled
> (not modulo) jitter, the band-fill regression test, and the two `shared.`-
> aliased call sites in sqs/sqspolicy. All 51 reconcile-scheduling sites now
> jitter; the only remaining `WithDelay` uses are non-reconcile (auth refresh,
> retention sweeps).

### A8 — CI and justfile hygiene (§6.2, §6.3, §6.5, §6.7, §7.4)

Each bullet is independent and safe:

1. `.golangci.yml`: add top-level `run: { build-tags: [integration] }`; raise
   `timeout` to `10m`. Expect new lint findings in `tests/integration/` — fix only
   mechanical ones (errorlint wrapping, bodyclose); anything judgment-y gets a
   `//nolint:<linter> // <reason>` (nolintlint requires the reason).
2. `.github/workflows/ci.yml`: add at top level
   `concurrency: { group: ci-${{ github.ref }}, cancel-in-progress: true }`.
   Replace the `go install golangci-lint` step (ci.yml:32) with
   `golangci/golangci-lint-action` pinned to the same version currently installed.
3. justfile `test` recipe (~line 216): add
   `-coverprofile=coverage.out -covermode=atomic`; ci.yml uploads `coverage.out`
   via `actions/upload-artifact`.
4. justfile `test-integration` (~line 492): add `-race`. If the CI integration job
   then exceeds its 50-min timeout, revert the ci.yml usage to a separate weekly
   `schedule:` job instead — do not raise the timeout past 50m.
5. justfile `ci` recipe (~line 796): change to
   `ci: lint fmt-check test build check-schema-drift test-integration` and add
   `check-schema-drift: generate-event-schemas && git diff --exit-code schemas/`
   (match the exact generated path CI checks at ci.yml:44-50); point ci.yml's
   schema-drift step at the new recipe so there is one definition.
6. justfile release preflight (~lines 1024-1028): add `dynamodbtable` to the
   praxis-storage set, `ekscluster ecscluster` to praxis-compute, `kmskey secret`
   to praxis-identity. Cross-check against each `cmd/praxis-*/main.go`'s registered
   drivers before committing.
7. justfile dedup (§7.4) — optional, larger: introduce
   `test-driver PKG: go test ./internal/drivers/{{PKG}}/... -race -p 1` and a single
   parameterized `_itest PATTERN TIMEOUT="10m":` containing the heartbeat loop once;
   rewrite the per-driver recipes as one-line aliases. Keep recipe *names* unchanged
   (CI and docs reference them).

**Verify:** `just ci` passes locally end-to-end, and a draft PR shows the same set of
green checks as before plus coverage artifact.

### A9 — Stop returning secret material from `GetInputs`; hash observed values (§1.1 part 3, §2.2)

**Files:** `internal/drivers/secret/driver.go:326-332`, `internal/drivers/secret/types.go`,
`internal/drivers/secret/drift.go`, `internal/drivers/secret/aws.go:118`; then the
same pattern for `ssmparameter` (`spec.value`) and rdsinstance/auroracluster
(`spec.masterUserPassword`, GetInputs only).

1. **GetInputs blanking** (all four drivers): before returning `state.Desired`,
   overwrite the sensitive field:

```go
	spec := state.Desired
	if spec.SecretString != "" {
		spec.SecretString = "(sensitive)"
	}
	return spec, nil
```

2. **Hash-compare instead of storing the observed secret** (secret driver only):
   - In `ObservedState` (`types.go:44-52`), replace `SecretString string` with
     `SecretStringSHA256 string`.
   - In the observe function (`aws.go:118` area), inside the existing
     `restate.Run` closure, hash before returning:
     `sha256.Sum256([]byte(value))` hex-encoded; never store the raw value.
   - In `drift.go`, compare `sha256hex(desired.SecretString) != observed.SecretStringSHA256`.
   - `SpecFromObserved` (import path) can no longer recover the value — set the
     spec's `SecretString` to empty and check how Import mode handles it; if
     Managed-mode import would then "correct" the secret to empty, guard drift
     correction on `desired.SecretString != ""` and document that imported secrets
     keep their existing value until a spec provides one. Read the Import handler
     fully before making this change; if the interaction is unclear, do only the
     GetInputs blanking (step 1) and leave the hash migration with a TODO.
   - State-compat: existing deployments have the old field persisted. The JSON
     rename means old `secretString` in stored observed state silently drops —
     acceptable (one spurious drift-check re-observe repopulates the hash), but
     say so in the commit message.

**Verify:** `tests/integration/secret_driver_test.go` currently asserts round-trips —
update assertions that read observed values; add an assertion that
`GetInputs`-backed output (`praxis get -o json` path, see
`internal/cli/get.go:354`) does not contain the plaintext.

> **Implementation status (this branch):** the leak is closed at the **CLI
> boundary, not the driver layer** — a post-review redesign. Driver-side
> blanking (this guide's step 1, tried first) permanently disabled the
> orchestrator's observe-before-act fast path: `provider.ObserveStoredState`
> DeepEquals `GetInputs` against the raw desired spec, so masked stored inputs
> never compare equal and every unchanged re-apply re-dispatched Provision
> (status flap, generation bump, spurious events). Masking both comparison
> sides instead would silently drop password-only changes. The landed design:
> drivers return `GetInputs` unmasked (with a comment saying why);
> `Client.GetResourceInputs` (internal/cli/client.go) masks via
> `types.MaskJSONPaths` from a CLI-side `sensitiveInputPaths` table; and
> `TestSensitiveInputPaths_MatchesProviderRegistry` (mirroring the existing
> kind-scopes guard) fails the build if that table ever drifts from the
> adapters' declarative `SensitiveFields` — one source of truth, so a future
> sensitive driver that sets `SensitiveFields` but forgets the CLI entry is a
> test failure, not a leak. Raw ingress callers see plaintext `GetInputs`; that
> is within the documented trust model (docs/AUTH.md — ingress callers can
> already mint full AWS credentials).
>
> Step 2 (hash the observed secret so the raw value never persists in Restate
> state/journal) remains **deferred**: it changes the persisted `ObservedState`
> shape (a state migration) and the import path's `SpecFromObserved`, and only
> hardens at-rest storage (defense-in-depth, finding §2.2). It should land as
> its own change with the import interaction worked through end to end.

### A.10 What "done" looks like

For every guide above, the acceptance bar is: (1) the specific verification listed
passes; (2) `just lint`, `gofmt -l .` clean, unit tests green; (3) the relevant
integration test run green; (4) no new `t.Skip`, no `//nolint` without a reason, no
API/behavior change beyond the finding's scope. If a guide's assumptions don't match
the code (a struct field name differs, a line has moved), STOP and re-read the
surrounding function rather than forcing the sketch to fit — the sketches encode
intent, not literal patches.
