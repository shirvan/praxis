# Praxis Glossary

> Key terms and concepts. Alphabetical. Cross-referenced to the docs.

---

## A

**Adapter** — Bridge between the orchestrator and a driver. Implements `provider.Adapter` interface: maps Kind → ServiceName, builds keys, decodes specs, dispatches Provision/Delete/Plan/Import calls. See [DRIVERS.md](DRIVERS.md).

**Account** — Named AWS credential configuration. Resolved from environment variables (`PRAXIS_ACCOUNT_*`). Supports `static`, `role`, and `default` credential sources. See [AUTH.md](AUTH.md).

**Apply** — CLI command that evaluates a CUE template, builds a DAG, and submits a deployment workflow. Idempotent — re-applying the same template is safe. See [CLI.md](CLI.md).

**Approval Gate** — A durable suspension point for deployments into a protected workspace: the workflow computes its plan, parks in `AwaitingApproval`, and resumes only when an operator runs `praxis approve` (or terminates on `praxis reject`). Decisions are recorded in the deployment event stream. See [OPERATORS.md](OPERATORS.md#approval-gates).

## C

**CloudEvent** — CNCF CloudEvents v1.0 envelope used for all Praxis events. Contains `specversion`, `id`, `source`, `type`, `time`, plus Praxis extension attributes (`deployment`, `workspace`, `generation`). See [EVENTS.md](EVENTS.md).

**CUE** — Configuration language used for templates and schemas. Non-Turing-complete, provides schema validation + templating in one language. See [TEMPLATES.md](TEMPLATES.md).

## D

**DAG** — Directed Acyclic Graph of resource dependencies. Built from `${resources.X.outputs.Y}` expressions in templates. Determines execution order via topological sort. See [ORCHESTRATOR.md](ORCHESTRATOR.md).

**Deployment** — A named instance of a template execution. Has a lifecycle: Pending → (AwaitingApproval →) Running → Complete/Failed/Cancelled. Tracked by `DeploymentStateObj` Virtual Object. See [ORCHESTRATOR.md](ORCHESTRATOR.md).

**Deployment Generation** — One apply run of a deployment key. Generation 1 is the first apply; every re-apply (including rollbacks) increments it. Each generation's full plan is snapshotted (last 10 retained) and is the unit point-in-time rollback targets. See [ORCHESTRATOR.md](ORCHESTRATOR.md).

**Drift** — Difference between desired state (spec) and observed state (what exists in AWS). Detected during reconciliation. Managed resources get drift corrected; observed resources only report. See [DRIVERS.md](DRIVERS.md).

**Driver** — A Restate Virtual Object managing one AWS resource type's full lifecycle. Implements 6 handlers: Provision, Import, Delete, Reconcile, GetStatus, GetOutputs. See [DRIVERS.md](DRIVERS.md).

**Driver Pack** — Group of related drivers deployed as one container/binary. Five packs: `praxis-storage`, `praxis-network`, `praxis-compute`, `praxis-identity`, `praxis-monitoring`. See [CODEBASE.md](CODEBASE.md).

## E

**EventBus** — Central event router hosted by `praxis-core`. Receives CloudEvents, validates, enriches, stores per-deployment, and fans out to notification sinks. See [EVENTS.md](EVENTS.md).

**Expression** — Template placeholder `${resources.NAME.outputs.FIELD}` resolved at dispatch time with actual outputs from completed resources. Must occupy full JSON values. See [TEMPLATES.md](TEMPLATES.md).

## F

**FieldDiff** — Atomic difference for a single field: `{Path, OldValue, NewValue}`. Used in plan output and drift detection. Path uses dot notation (e.g., `spec.cidrBlock`). See [DRIVERS.md](DRIVERS.md).

## G

**Generation** — Monotonically increasing counter on a deployment. Incremented on each re-apply. Enables re-apply semantics without creating new deployment keys. See [ORCHESTRATOR.md](ORCHESTRATOR.md).

## H

**Hydration** — Process of replacing `${resources.X.outputs.Y}` expressions with actual typed values from completed resources. Happens at dispatch time in the orchestrator. See [ORCHESTRATOR.md](ORCHESTRATOR.md).

## I

**Import** — Adopting an existing AWS resource into Praxis management. Captures observed state as both desired and observed (no drift on first reconcile). See [DRIVERS.md](DRIVERS.md).

## K

**Key** — Natural identifier for a resource Virtual Object. Format depends on scope: Global (`name`), Region (`region~name`), Custom (`vpcId~groupName`). See [DRIVERS.md](DRIVERS.md).

**KeyScope** — Determines how a resource key is constructed. Three scopes: `Global` (name only), `Region` (region~name), `Custom` (resource-specific compound key). See [DRIVERS.md](DRIVERS.md).

## L

**Lifecycle Rules** — Template-level guards: `preventDestroy` (blocks deletion), `ignoreChanges` (skip drift for listed fields), `orphanPolicy`, `maxRetries`, `timeouts`. See [TEMPLATES.md](TEMPLATES.md).

## M

**Mode** — Resource management mode. `Managed`: full lifecycle with drift correction. `Observed`: import-only, read-only, drift reported but not corrected. See [DRIVERS.md](DRIVERS.md).

## O

**Orchestrator** — Deployment execution engine. Takes a compiled plan, reconstructs the DAG, dispatches resources in topological order with maximum parallelism, hydrates expressions. See [ORCHESTRATOR.md](ORCHESTRATOR.md).

**Outputs** — Key-value pairs produced by a driver after Provision/Import. Fed back to the orchestrator for expression hydration (e.g., `{arn, resourceId, vpcId}`). See [DRIVERS.md](DRIVERS.md).

## P

**Plan** — Dry-run showing what changes would be made. Computes field-level diffs for each resource (Create/Update/Delete/NoOp). See [CLI.md](CLI.md).

**Policy** — CUE constraint applied during template evaluation. Scopes: `global` (all templates) or `template:<name>` (specific template). Violations detected via baseline-comparison. See [TEMPLATES.md](TEMPLATES.md).

**Point-in-Time Rollback** — `praxis rollback <key> --to <generation>`: replaying a stored known-good generation's plan to revert a deployment — distinct from `praxis delete --rollback`, which cleans up a failed deployment by deleting its resources. See [OPERATORS.md](OPERATORS.md#point-in-time-rollback).

**Protected Workspace** — A workspace configured with `Protected: true` (`praxis create workspace --protected`). Every deployment into it must pass an approval gate before any resource is dispatched. See [OPERATORS.md](OPERATORS.md#approval-gates).

**Provider Schema** — CUE schema in `schemas/aws/` defining the shape of a resource type. Templates are unified against these for validation. See [TEMPLATES.md](TEMPLATES.md).

## R

**Reconcile** — Periodic check (5-min durable timer) comparing desired vs observed state. Managed resources get drift corrected. Observed resources report drift only. See [DRIVERS.md](DRIVERS.md).

**Restate** — Durable execution platform. Provides Virtual Objects, workflows, journals, timers, and exactly-once semantics. Praxis services are stateless; Restate owns all state. See [PRAXIS_ARCHITECTURE.md](PRAXIS_ARCHITECTURE.md).

**Resource** — A managed infrastructure entity (e.g., S3 bucket, VPC, EC2 instance). Lifecycle: Pending → Provisioning → Ready ↔ Error → Deleting → Deleted. See [DRIVERS.md](DRIVERS.md).

## S

**Sink** — External notification destination. Types: `webhook` (HTTP POST with CloudEvent JSON body) and `restate_rpc` (Restate service-to-service send — the extension hook for custom consumers). Filtered by event type/category/severity/workspace/deployment. See [EVENTS.md](EVENTS.md).

**SSM** — AWS Systems Manager Parameter Store. Used for secret resolution via `ssm:///path/to/param?sensitive=true` URIs in templates. Batch-resolved during compilation. See [TEMPLATES.md](TEMPLATES.md).

**State** — Per-resource durable state stored under single key `"state"` in the Virtual Object. Contains: Desired spec, Observed state, Outputs, Status, Mode, Error, Generation. See [DRIVERS.md](DRIVERS.md).

## T

**Template** — CUE file declaring variables and resources for infrastructure. Evaluated through multi-phase pipeline: CUE parse → schema unify → policy apply → SSM resolve → DAG build. See [TEMPLATES.md](TEMPLATES.md).

**Terminal Error** — Error that should NOT be retried (validation, conflict, not-found). Wrapped as `restate.TerminalError()` INSIDE `restate.Run()` callbacks. See [ERRORS.md](ERRORS.md).

## V

**Virtual Object** — Restate primitive: durable, key-addressed actor with exclusive-writer and shared-reader handlers. One per resource instance and one per deployment. See [PRAXIS_ARCHITECTURE.md](PRAXIS_ARCHITECTURE.md).

## W

**Workspace** — Named environment binding deployments to shared defaults (region, account). Created via `praxis create workspace`. See [AUTH.md](AUTH.md).

**Workflow** — Restate primitive for run-once-per-key durable execution. Used for `DeploymentWorkflow` (apply) and `DeploymentDeleteWorkflow` (delete). See [ORCHESTRATOR.md](ORCHESTRATOR.md).
