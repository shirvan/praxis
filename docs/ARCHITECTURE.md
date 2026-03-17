# Praxis Architecture

> **See also:** [Drivers](DRIVERS.md) | [Orchestrator](ORCHESTRATOR.md) | [Templates](TEMPLATES.md) | [CLI](CLI.md) | [Operators](OPERATORS.md) | [Developers](DEVELOPERS.md)

---

## Overview

Praxis is a declarative infrastructure automation platform that manages cloud resources through continuous reconciliation. It draws inspiration from Kubernetes controllers and Crossplane — declare what you want, and the system converges to make it so — but replaces the Kubernetes control plane with [Restate](https://restate.dev), a durable execution engine.

The result: a system with the same reconciliation semantics (drift detection, self-healing, dependency-aware orchestration) that runs as a set of lightweight services in Docker Compose instead of requiring a full cluster.

---

## System Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│                         USER / CLI / API                         │
│                                                                  │
│   praxis apply webapp.cue        praxis plan stack.cue           │
│   praxis get S3Bucket/my-bucket  praxis delete my-stack          │
│   praxis import S3Bucket --id x  praxis observe my-stack         │
└──────────────────────────┬───────────────────────────────────────┘
                           │ HTTP/JSON via Restate ingress
                           ▼
┌──────────────────────────────────────────────────────────────────┐
│                        PRAXIS CORE                               │
│                                                                  │
│  ┌─────────────────┐  ┌──────────────┐  ┌──────────────────────┐ │
│  │ Command Service │  │   Template   │  │    Deployment        │ │
│  │ (Restate Basic  │  │   Engine     │  │    Orchestrator      │ │
│  │  Service)       │  │ (CUE + CEL)  │  │ (Workflows + VOs)   │ │
│  └────────┬────────┘  └──────────────┘  └──────────┬───────────┘ │
│           │                                        │             │
│  ┌────────┴──────────────────────────┐  ┌──────────┴───────────┐ │
│  │ Template Registry + Policy Engine │  │ Deployment State,    │ │
│  │ (Virtual Objects)                 │  │ Index, Events (VOs)  │ │
│  └───────────────────────────────────┘  └──────────────────────┘ │
└──────────────────────────┬───────────────────────────────────────┘
                           │ Restate RPC (durable, exactly-once)
                           ▼
┌──────────────────────────────────────────────────────────────────┐
│                   DRIVER SERVICES (per resource type)            │
│                                                                  │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐                 │
│  │ S3 Driver  │  │ SG Driver  │  │ ... more   │                 │
│  │ (container)│  │ (container)│  │ (container) │                │
│  └────────────┘  └────────────┘  └────────────┘                 │
└──────────────────────────┬───────────────────────────────────────┘
                           │
                    ┌──────┴──────┐
                    │  AWS APIs   │
                    └─────────────┘
```

---

## Components

### Restate — The Execution Engine

Restate is the backbone that makes everything work. It provides:

- **Durable execution** — every operation is journaled. If a service crashes mid-call, Restate replays the journal from the last checkpoint without re-executing completed steps.
- **Virtual Objects** — stateful, key-addressable entities with exclusive (single-writer) and shared (concurrent-read) handler modes. Each cloud resource is one Virtual Object.
- **Built-in K/V state** — each Virtual Object has its own key-value store, eliminating the need for an external database.
- **Durable timers** — survive process restarts. Used for reconciliation scheduling.
- **Exactly-once RPC** — service-to-service calls are journaled and deduplicated.

Praxis does not use Restate as a simple message broker. It uses Restate as the **runtime** — state storage, concurrency control, crash recovery, and inter-service communication all flow through it.

### Praxis Core

The central coordination service. It hosts:

- **Command Service** — a Restate Basic Service that receives user commands (apply, plan, delete, import) and orchestrates the response. Evaluates templates, builds dependency graphs, and submits deployment workflows.
- **Template Engine** — validates and evaluates CUE templates, resolves CEL expressions, enforces policy constraints, and resolves SSM secret references.
- **Deployment Orchestrator** — Restate Workflows that execute apply and delete operations. The scheduler dispatches resources in dependency order with maximum parallelism.
- **Template Registry** — a Virtual Object that stores registered templates with metadata, digest tracking, and shallow rollback.
- **Policy Registry** — a Virtual Object that stores CUE-based policy constraints scoped globally or per template.
- **Deployment State / Index / Events** — Virtual Objects that persist deployment lifecycle state, provide listing indexes, and record event streams.

Core runs as a single container. All its Restate services register under one deployment endpoint.

### Driver Services

Each cloud resource type is managed by a dedicated driver service — a standalone container hosting Restate Virtual Objects. The S3 driver manages S3 buckets. The SecurityGroup driver manages EC2 security groups. Each driver:

- Runs independently with its own deployment lifecycle
- Registers with Restate as a separate service
- Implements a standard handler contract: `Provision`, `Import`, `Delete`, `Reconcile`, `GetStatus`, `GetOutputs`
- Stores all resource state in Restate's built-in K/V store
- Handles its own rate limiting for AWS API calls
- Has zero knowledge of other drivers, dependency graphs, or deployments

This design makes drivers simple to build, test, and deploy. A driver is ~500 lines of Go implementing six handlers around an AWS SDK wrapper.

### CLI

The `praxis` CLI is a standalone Go binary built with Cobra. It talks directly to Restate's ingress HTTP API — there is no dedicated Praxis API server. Write commands (`apply`, `plan`, `delete`, `import`) route to the Command Service. Read commands (`get`, `list`, `observe`) query Virtual Objects directly.

---

## Design Tradeoffs

### Why Restate Instead of Kubernetes

Kubernetes provides reconciliation, state management, and scheduling — but it requires operating a cluster (etcd, API server, controller manager, scheduler). For teams that already run Kubernetes, Crossplane is a natural choice. For teams that don't — or that want infrastructure management without cluster overhead — Praxis offers the same reconciliation model on a simpler runtime.

Restate gives Praxis the properties typically associated with Kubernetes controllers:
- **State management** → Virtual Object K/V store (vs etcd)
- **Single-writer per resource** → Exclusive handlers (vs controller leader election)
- **Crash recovery** → Journal replay (vs controller restart + re-list)
- **Periodic reconciliation** → Durable timers (vs informer watches + work queues)

The tradeoff: Restate is a younger project than Kubernetes with a smaller ecosystem. Praxis bets that for the infrastructure management use case, the simplicity advantage outweighs the ecosystem breadth.

### Why CUE + CEL Instead of HCL or YAML

CUE merges types, constraints, defaults, and values into a single lattice. A CUE schema is also a validator, a default provider, and a composition target — all in one language. This means platform teams can define rich, validated templates that end users fill in without learning the full language.

CEL provides a safe, non-Turing-complete expression language for runtime references — wiring one resource's outputs into another's inputs. It's the same expression language used by Kubernetes, kro, and Google Cloud IAM.

The tradeoff: CUE has a steeper learning curve than YAML. Praxis mitigates this by having platform teams write CUE templates while end users mostly interact through CLI variables and pre-registered templates.

### Why Centralized Orchestration

Praxis uses a centralized orchestrator in Core rather than distributed choreography between drivers. Core resolves the dependency graph, dispatches resources, collects outputs, and manages the deployment lifecycle.

Benefits:
- Drivers stay simple — they implement CRUD and know nothing about dependencies
- Deployment state lives in one place for consistent observability
- Failure handling is centralized — skip dependents, report clear errors
- Rollback has a single coordination point

Tradeoff: Core is a coordination bottleneck. Every deployment flows through it. This is acceptable at Praxis's current scale and simplifies the system significantly.

### Why Virtual Objects for Resources

Instead of a single driver service managing all instances of a resource type through a shared database, each resource instance is its own Virtual Object keyed by a natural identifier (e.g., `my-bucket` for S3).

Benefits:
- **Single-writer guarantee** — no distributed locking needed
- **Built-in state** — no external database to operate
- **Natural addressing** — `S3Bucket/my-bucket` maps directly to a Virtual Object key
- **Stateless services** — driver containers hold no state; Restate holds it all. Scale horizontally, restart freely.

Tradeoff: state lives in Restate's storage layer, not a traditional database. Restate supports S3-backed snapshots for disaster recovery.

### Why Separate Binaries per Driver

Each driver is a standalone binary and container rather than plugins loaded into a monolithic process. This means:
- Deploy only the drivers you need
- Update one driver without restarting others
- Independent scaling per resource type
- Fault isolation — one driver crashing doesn't affect others

Tradeoff: more containers to manage than a single process. Docker Compose and similar tools make this manageable.

---

## Data Flow

### Apply (Provision)

```
1. User runs: praxis apply webapp.cue --account local --var env=dev --key my-stack
2. CLI reads template file, sends ApplyRequest to Command Service via Restate ingress
3. Command Service runs the template pipeline:
   a. Resolve template source (inline or from registry)
   b. Load policies (global + template-scoped)
   c. CUE evaluation — validate against schemas, apply defaults, inject variables
   d. SSM resolution — resolve secret references (journaled)
   e. CEL pass 1 — resolve variables.* expressions
   f. DAG construction — parse resource dependencies from CEL expressions
4. Command Service initializes DeploymentState, submits DeploymentWorkflow
5. Workflow runs eager scheduler:
   a. Dispatch resources whose dependencies are met
   b. Wait for any running resource to complete
   c. Hydrate downstream specs with outputs (CEL pass 2)
   d. Repeat until all resources are complete or failed
6. CLI polls DeploymentState until terminal status
```

### Plan (Dry-Run)

Same as Apply through step 3, then diffs each resource spec against the driver's current state to produce a per-field change summary. No workflow is submitted.

### Delete

```
1. User runs: praxis delete Deployment/my-stack --yes
2. Command Service reads DeploymentState, submits DeploymentDeleteWorkflow
3. Delete workflow builds reverse dependency order from stored metadata
4. Deletes resources in reverse topological order (parallel where safe)
5. Finalizes DeploymentState as Deleted
```

### Reconcile

```
1. Driver schedules a durable timer after each Provision or Reconcile (default: 5 min)
2. Timer fires → Reconcile handler runs
3. Driver describes actual cloud state, compares against desired spec
4. Managed mode: corrects drift by re-applying configuration
5. Observed mode: reports drift without correcting
6. Schedules next timer
```

---

## Resource Lifecycle

```
Pending → Provisioning → Ready ⟲ (Reconcile loop)
                            ↓
                          Error ← drift / failures
                            ↓
Ready → Deleting → Deleted
```

| Status | Description |
|--------|-------------|
| `Pending` | Declared but not yet provisioned |
| `Provisioning` | Provision handler is executing |
| `Ready` | Exists and matches desired state |
| `Error` | Provision or reconciliation failed — check the error field |
| `Deleting` | Delete handler is executing |
| `Deleted` | Removed (tombstone preserved for status queries) |

### Resource Modes

| Mode | Behavior |
|------|----------|
| **Managed** | Full lifecycle — provision, reconcile, correct drift, delete |
| **Observed** | Import-only — detect and report drift but never modify the resource |

---

## What Praxis Is Not

- **Not a Kubernetes replacement.** Praxis manages cloud infrastructure resources, not container workloads.
- **Not a CI/CD pipeline.** Praxis is the target of a pipeline, not the pipeline itself.
- **Not multi-cloud (yet).** 0.1.0 targets AWS only. The driver model supports multi-cloud — the architecture is ready, the implementations are not.
- **Not multi-tenant.** The 0.1.0 trust model is operator-managed. There is no built-in auth or RBAC.

---

## Further Reading

- [Drivers](DRIVERS.md) — how drivers work, how to build one
- [Orchestrator](ORCHESTRATOR.md) — deployment workflows, DAG scheduling, state management
- [Templates](TEMPLATES.md) — CUE + CEL template system, registry, policies
- [CLI](CLI.md) — command reference and usage patterns
- [Operators](OPERATORS.md) — deployment, configuration, monitoring
- [Developers](DEVELOPERS.md) — building, testing, contributing
