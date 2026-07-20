# Codebase

> Directory structure, key files, and module organization for navigating the Praxis codebase.

---

## Top-Level Layout

```
praxis/
├── cmd/                    # Binary entrypoints (7 binaries)
├── internal/               # Private packages (core business logic)
│   ├── cli/                # Cobra CLI commands
│   ├── core/               # Coordination layer (14 subpackages)
│   ├── drivers/            # AWS resource drivers (51 packages)
│   ├── eventing/           # CloudEvents contracts shared with drivers
│   └── infra/              # AWS client wrappers, rate limiting
├── pkg/types/              # Public shared types
├── schemas/                # CUE validation schemas
│   ├── aws/                # Per-resource schemas (20 services)
│   ├── data/               # Data source lookup schema
│   ├── events/             # CloudEvent schemas
│   └── notifications/      # Sink/retention schemas
├── tests/integration/      # In-process integration tests (60+ files)
├── tests/acceptance/       # Compiled CLI + production process topology
├── deploy/quickstart/      # No-clone, image-based alpha evaluation bundle
├── scripts/                # Alpha artifact build and verification
├── docs/                   # Documentation (see INDEX.md)
├── skills/                 # Agent skills (how-to procedures)
├── examples/               # Example templates
├── charts/praxis/          # Helm chart
├── docker-compose.yaml     # Local dev stack
├── justfile                # Build/test/ops recipes
└── go.mod                  # Go module
```

## Binaries (`cmd/`)

| Binary | Host Port (compose) | Purpose |
|--------|------|---------|
| `praxis` | — | CLI (Cobra), connects to Core via Restate ingress |
| `praxis-core` | 9083 | Command Service, Orchestrator, Registries, Auth/Workspace, EventBus + EventStore + Sinks |
| `praxis-storage` | 9081 | S3, EBS, RDS, Aurora, DB SubnetGroup, DB ParamGroup, SNS, SQS, ECR drivers |
| `praxis-network` | 9082 | VPC, Subnet, SG, NACL, RouteTable, IGW, NAT GW, EIP, VPC Peering, ALB, NLB, TG, Listener, ListenerRule, ACM, Route53 |
| `praxis-compute` | 9084 | EC2, AMI, KeyPair, Lambda, LambdaLayer, LambdaPerm, ESM |
| `praxis-identity` | 9085 | IAM Role, User, Group, Policy, Instance Profile |
| `praxis-monitoring` | 9087 | CloudWatch Log Group, Metric Alarm, Dashboard |

## Core Subpackages (`internal/core/`)

| Package | Purpose |
|---------|---------|
| `auth/` | Account configuration registry, credential sources |
| `authservice/` | Auth Service Virtual Object (credential caching, STS) |
| `command/` | PraxisCommandService handlers (apply, plan, delete, import, policy, template) |
| `config/` | Environment-based configuration loader |
| `cuevalidate/` | CUE schema validation utilities |
| `dag/` | DAG graph, topological sort, scheduler, dependency parser |
| `diff/` | Plan diff engine (field-level comparison) |
| `jsonpath/` | JSON path get/set utilities |
| `orchestrator/` | Workflows, deployment state, events, sinks, hydrator, indexes |
| `provider/` | Adapter registry (Kind → ServiceName), 51 adapter files |
| `registry/` | Template + Policy registries (Restate Virtual Objects) |
| `resolver/` | SSM parameter resolution |
| `template/` | CUE evaluation engine |
| `workspace/` | Workspace Service + Index |

## Driver Package Layout (`internal/drivers/{resource}/`)

Each of the 51 driver packages follows this pattern:

```
{resource}/
  types.go          — Spec, Outputs, and ObservedState types
  aws.go            — API interface + real implementation
  drift.go          — HasDrift(), ComputeFieldDiffs()
  generic.go        — Resource operations + generic kernel descriptor
  generic_test.go   — Shared lifecycle conformance suite
  driver_test.go    — Resource-specific lifecycle tests, when needed
  aws_test.go       — Error classification tests
  drift_test.go     — Drift detection tests
```

Shared driver code:
- `internal/drivers/kernel/` — The one lifecycle implementation and durable state envelope
- `internal/driverpack/genericbinding/` — The one production Restate binding path
- `internal/drivers/tags.go` — Tag comparison helpers
- `internal/drivers/drift_events.go` — Drift event emission
- `internal/drivers/autherr.go` — Auth error classifiers
- `internal/drivers/awserr/classify.go` — Shared AWS error classifiers

## Provider Adapters (`internal/core/provider/`)

One adapter per driver, plus shared infrastructure:
- `{resource}_adapter.go` — Implements `provider.Adapter` interface
- `{resource}_adapter_test.go` — Spec decoding tests
- `registry.go` — Central registry, `NewRegistry()` wires all 51 adapters
- `keys.go` — Key scope types, `JoinKey()`/`SplitKey()`
- `adapter.go` — Adapter interface definition

## Shared Types (`pkg/types/`)

| File | Contents |
|------|----------|
| `resource.go` | ResourceStatus, Mode, ImportRef, ReconcileResult |
| `deployment.go` | DeploymentStatus, DeploymentState, DeploymentDetail |
| `dag.go` | ResourceNode, PlanResource |
| `diff.go` | DiffOperation, FieldDiff, PlanResult |
| `policy.go` | Policy, PolicyScope, PolicyRecord |
| `template.go` | TemplateRef, TemplateRecord, VariableSchema |
| `contract.go` | Request/Response DTOs (Apply, Plan, Delete, Import, Template, Policy) |
| `conditions.go` | Resource conditions (Ready, Provisioned, Healthy, DriftFree) |
| `status.go` | StatusResponse type |
| `errorcode.go` | Stable machine-readable error codes |

## Important Entry Points

| Task | Start Here |
|------|------------|
| Follow a deploy request | `internal/cli/deploy.go` → `internal/core/command/handlers_apply.go` → `internal/core/orchestrator/workflow.go` |
| Understand a driver | `internal/drivers/{resource}/generic.go` → `types.go` → `aws.go` → `drift.go` → `internal/drivers/kernel/` |
| Template evaluation | `internal/core/template/engine.go` → `internal/core/command/pipeline.go` |
| DAG scheduling | `internal/core/dag/graph.go` → `scheduler.go` → `parser.go` |
| Error handling | `internal/drivers/awserr/classify.go` → `internal/core/template/errors.go` |
| Event flow | `internal/core/orchestrator/event_bus.go` → `event_store.go` → `notification_sinks.go` |
| Auth flow | `internal/core/authservice/auth_service.go` → `internal/core/auth/registry.go` |

## See Also

- [INDEX.md](INDEX.md) — Documentation directory
- [PRAXIS_ARCHITECTURE.md](PRAXIS_ARCHITECTURE.md) — System design patterns
- [DEVELOPERS.md](DEVELOPERS.md) — Building and testing
- [DRIVERS.md](DRIVERS.md) — Driver contract details
