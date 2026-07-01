# Praxis

**Declarative infrastructure automation — without the cluster.**

[Why Praxis](#why-praxis) · [Get Started](#get-started) · [Docs](#documentation) · [Future](docs/FUTURE.md)

---

> Praxis is in alpha, with limited real world testing.

Infrastructure-as-code tools stop paying attention the moment `apply`
returns. A security group opened "temporarily" in the console, a tag removed
by a script, a queue policy deleted by hand — none of it surfaces until the
next time someone runs `plan`. The tools that do watch continuously, like
Crossplane and Kubernetes operators, require running a cluster just to
manage cloud resources.

Praxis does both halves without the cluster. You declare resources in typed,
validated [CUE](https://cuelang.org/) templates; Praxis provisions them in
dependency order, then keeps checking: every resource is re-compared against
reality on a five-minute interval, drift is corrected (or just reported —
your choice per resource), and every change lands in an audit event stream.
The whole stack runs from Docker Compose on a laptop, on Kubernetes, or on
Restate Cloud.

```bash
# Declare a bucket in a CUE template (typed, validated, with defaults)
cat > bucket.cue <<'CUE'
resources: archive: {
    apiVersion: "praxis.io/v1"
    kind:       "S3Bucket"
    metadata: name: "orders-archive"
    spec: {
        region:     "us-east-1"
        versioning: true
        tags: team: "payments"
    }
}
CUE

praxis plan bucket.cue --account prod      # see exactly what would change
praxis deploy bucket.cue --account prod --key orders --wait

# From now on the bucket is reconciled every five minutes. Disable
# versioning in the console and it's re-enabled, with a drift event logged.
praxis observe Deployment/orders
```

Praxis is built on [Restate](https://restate.dev), a durable execution
engine. Every AWS API call is journaled: if anything crashes mid-provision,
execution resumes where it stopped — no duplicate resources, no half-applied
state. Each resource is a single-writer stateful object, so there are no
racing updates and no distributed locks.

For the full reasoning behind the design, read the
[Architecture document](docs/PRAXIS_ARCHITECTURE.md).

```mermaid
graph TD
    CLI["Praxis CLI / API"] --> Restate["Restate<br/>durable execution engine"]
    Restate --> Core["Praxis Core<br/>commands, workflows, templates, events"]
    Core -->|"Restate RPC"| Drivers

    subgraph Drivers["Driver Packs"]
        Storage
        Network
        Compute
        Identity
        Monitoring
    end

    Storage --> AWS["AWS APIs"]
    Network --> AWS
    Compute --> AWS
    Identity --> AWS
    Monitoring --> AWS
```

---

## Why Praxis

Managing cloud infrastructure today means choosing between extremes:

- **Terraform** gives you plan-and-apply but no continuous reconciliation, no drift correction, and state file contention at scale.
- **Crossplane** gives you Kubernetes-native reconciliation but requires operating a full cluster just to manage cloud resources.
- **CDK / Pulumi** give you real programming languages but the same imperative plan-apply model underneath.

None of them let you declare infrastructure, have it continuously converged, and run it all from a Docker Compose stack.

### What Praxis Does Differently

| | Terraform | Crossplane | Praxis |
| --- | --- | --- | --- |
| **Execution model** | Plan → Apply (imperative, manual) | Continuous reconciliation | Continuous reconciliation |
| **Runtime requirement** | CLI + state backend | Kubernetes cluster | Restate server (single binary) |
| **Drift detection** | Manual (`terraform plan`) | Automatic | Automatic |
| **Execution guarantee** | None (can leave partial state) | At-least-once | **Exactly-once** (journaled) |
| **Crash recovery** | Manual intervention | Controller restart + re-reconcile | Automatic journal replay |
| **Dependency resolution** | Provider-determined | Composition functions | DAG from output expressions |
| **Template language** | HCL | YAML + Compositions | CUE |
| **Extension model** | Go providers (complex SDK) | Go controllers (complex SDK) | Restate Virtual Objects (any language, no fork) |

### Key Capabilities

**Durable Execution.** Every AWS API call is journaled by Restate. If a driver crashes mid-provision, execution resumes from the journal — no duplicate calls, no partial state.

**Continuous Reconciliation.** Drivers automatically detect and correct configuration drift on a 5-minute interval using Restate's durable timers. No external cron, no polling infrastructure.

**Single-Writer Guarantee.** Each resource is a Restate Virtual Object with exclusive handler execution. No racing updates, no distributed locks, no optimistic concurrency conflicts.

**Dependency-Aware Orchestration.** Templates declare cross-resource dependencies via output expressions (`${resources.<name>.outputs.<field>}`). The orchestrator builds a DAG and dispatches resources with maximum parallelism as dependencies complete.

**Plan Before Apply.** Preview exactly what would change before committing — per-field diffs for every resource, including those with cross-resource expression references. Expression-bearing resources are resolved at plan time using live driver state, just like `terraform plan`.

**Import Existing Resources.** Adopt cloud resources already running in your account. Praxis captures their current state as a baseline and begins managing or observing them.

**Data Sources.** Reference existing cloud resources in templates without managing them. A `data` block performs read-only lookups that inject outputs (VPC IDs, ARNs, CIDR blocks) into managed resource specs — no state stored, no lifecycle tracked. Currently supported for VPC, Subnet, Security Group, S3 Bucket, IAM Role, and Route 53 Hosted Zone.

**CUE Templates.** Platform teams define typed, validated templates in CUE. End users fill in variables. Output expressions wire resource outputs into downstream specs. Policy constraints enforce organizational standards via CUE unification.

**Lifecycle Protection.** Mark resources with `preventDestroy` to block accidental deletion, or `ignoreChanges` to let external systems co-manage specific fields without Praxis fighting for control.

**Approval Gates.** Mark a workspace as protected and every deployment into it suspends — durably, at zero cost, for as long as it takes — until an operator runs `praxis approve` or `praxis reject`. Decisions land in the deployment event stream as an audit trail of who approved what, when, and why.

**Point-in-Time Rollback.** Every apply snapshots its plan as a generation. `praxis rollback <key> --to <generation>` replays a known-good generation: changed specs are reverted, resources added since are deleted, resources removed since are re-provisioned — and the rollback itself becomes a new, roll-back-able generation.

**Lightweight Operations.** The entire stack runs in Docker Compose. No etcd, no API server, no cluster to maintain. Drivers are grouped by AWS domain into independent driver packs that register with Restate.

**Extensible Without Forking.** Praxis runs on [Restate](https://restate.dev), and Restate doesn't distinguish between built-in and external services. Write a custom driver in Python, TypeScript, Go, Java, Kotlin, or Rust from your own repository, register it with the same Restate instance, and it participates in DAG orchestration, output expression hydration, state tracking, and event streaming alongside built-in drivers. No plugin SDK, no fork, no code changes to Praxis. See the [Extending Guide](docs/EXTENDING.md).

---

## Get Started

Praxis runs anywhere Restate runs — a single Docker Compose stack on your laptop, a Kubernetes cluster, or fully managed on Restate Cloud. Pick the path that fits your situation.

### Local Development

The fastest way to try Praxis. Docker Compose brings up Moto (mock AWS), Restate, Praxis Core, and all driver packs.

#### Prerequisites

- [Docker](https://www.docker.com/) + Docker Compose
- [just](https://github.com/casey/just) (task runner)
- [Go](https://go.dev/) >= 1.25 (for building from source)

#### Start the Stack

```bash
git clone https://github.com/shirvan/praxis.git
cd praxis

# Create the operator environment file
cp .env.example .env

# Start Moto + Restate + Praxis Core + drivers, then register services
just up
```

#### Use the CLI

```bash
# Build the CLI
just build-cli

# --- Operator: register a template ---
praxis template register webapp.cue --description "Web application stack"
praxis template list
praxis template describe webapp

# --- User: deploy from a registered template ---
# Preview changes (dry-run)
praxis deploy webapp --account local --var env=dev --dry-run

# Deploy with variables
praxis deploy webapp --account local --var env=dev --key my-webapp --wait

# Deploy with a variables file
praxis deploy webapp --account local -f vars.json --key my-webapp --wait

# --- Common operations ---
praxis get Deployment/my-webapp          # Check deployment status
praxis list deployments                  # List all deployments
praxis observe Deployment/my-webapp      # Follow deployment events
praxis delete Deployment/my-webapp --yes --wait

# --- Operator: inline CUE (development/testing) ---
praxis plan webapp.cue --account local --var env=dev
praxis deploy webapp.cue --account local --var env=dev --key my-webapp --wait
```

### Centralized Deployment (Kubernetes)

For team and production use, deploy Praxis on Kubernetes with the Helm chart published to GitHub Container Registry. The chart deploys all Praxis components and optionally bundles a Restate instance — or you can point to an external one (like [Restate Cloud](https://restate.dev/cloud/)).

```bash
# Deploy with bundled Restate
helm install praxis oci://ghcr.io/shirvan/charts/praxis \
  --namespace praxis-system --create-namespace

# Or deploy against Restate Cloud (no bundled Restate)
helm install praxis oci://ghcr.io/shirvan/charts/praxis \
  --namespace praxis-system --create-namespace \
  --set restate.enabled=false \
  --set restate.external.ingressUrl=https://<env>.dev.restate.cloud:8080 \
  --set restate.external.adminUrl=https://<env>.dev.restate.cloud:9070

# Wait for readiness
kubectl -n praxis-system wait --for=condition=ready pod \
  -l app.kubernetes.io/part-of=praxis --timeout=120s

# (Optional) Enable autoscaling for driver packs
helm upgrade praxis oci://ghcr.io/shirvan/charts/praxis \
  --namespace praxis-system \
  --set drivers.network.autoscaling.enabled=true \
  --set drivers.compute.autoscaling.enabled=true
```

Service registration with Restate is handled automatically by a post-install hook. Raw YAML manifests (without Helm) are available in [`examples/ops/k8s/`](examples/ops/k8s/) for environments where Helm is not an option.

#### Production Readiness

Restate is built for production workloads:

- **State & log backups** — Self-hosted Restate stores its log and state snapshots in S3 (or S3-compatible storage) for durable backup and recovery.
- **High availability** — Multi-node Restate clusters with replicated log storage for fault tolerance.
- **Restate Cloud** — Fully managed HA with zero infrastructure overhead.

See the [Operator Guide](docs/OPERATORS.md) for Praxis-specific deployment, configuration, and monitoring details. See the [Restate documentation](https://docs.restate.dev/category/restate-server) for Restate server configuration, HA setup, and backup strategies.

### Talk to Praxis

Praxis offers three ways to interact — pick what fits your workflow.

#### CLI

The `praxis` binary is the primary interface. Every command goes through the Restate ingress endpoint.

```bash
praxis deploy webapp --account prod --var env=staging --key my-webapp --wait
praxis get Deployment/my-webapp
praxis plan webapp.cue --account prod --var env=prod
```

See the [CLI Reference](docs/CLI.md) for the full command set.

#### API

Everything the CLI does is an HTTP call to the Restate ingress. Integrate Praxis into CI/CD pipelines, scripts, or internal tools directly:

```bash
curl -X POST http://localhost:8080/PraxisCommandService/Apply \
  -H 'content-type: application/json' \
  -d '{"template": "webapp", "account": "prod", "variables": {"env": "staging"}}'
```

See the [API Reference](docs/API.md) and the [OpenAPI spec](api/openapi.yaml).

#### AI Agents

Praxis is built to be driven by any AI agent harness (Claude Code, Copilot,
Cursor, your own loop) — no embedded assistant required:

- Every CLI command supports `-o json`; errors come back as a JSON envelope
  with [stable exit codes](docs/CLI.md#exit-codes).
- `praxis list schemas` and `praxis get schema <Kind>` expose the full CUE
  spec for every resource kind, offline.
- The HTTP API is documented in [docs/API.md](docs/API.md) with a
  machine-readable [OpenAPI spec](api/openapi.yaml).
- [`AGENTS.md`](AGENTS.md) is the repo entry point for agents, with
  step-by-step [skills](skills/MANIFEST.md) — including
  [migrating Terraform/CloudFormation/Crossplane to CUE](skills/migrate-template/SKILL.md).

### Plan Output

```text
Praxis will perform the following actions:

  # S3Bucket "my-bucket" will be created
  + resource "S3Bucket" "my-bucket" {
      + bucketName  = "my-bucket"
      + region      = "us-east-1"
      + versioning  = true
      + tags {
          + env = "staging"
        }
    }

  # SecurityGroup "vpc-0abc123~web-sg" will be updated in-place
  ~ resource "SecurityGroup" "vpc-0abc123~web-sg" {
      ~ description = "old desc" => "new desc"
      - sslPolicy  = "ELBSecurityPolicy-2016-08"
    }

Plan: 1 to create, 1 to update, 0 to delete, 2 unchanged.
```

Symbols: `+` create, `~` update, `-` delete. Fields within an update that change to empty are shown as deletions with the `-` prefix.

---

## AWS Coverage

51 drivers across five domains:

| Domain | Resources |
|--------|-----------|
| **Network** (18) | VPC, Security Group, Subnet, Route Table, Internet Gateway, NAT Gateway, Network ACL, Elastic IP, VPC Peering, Hosted Zone, DNS Record, Health Check, ALB, NLB, Target Group, Listener, Listener Rule, ACM Certificate |
| **Compute** (11) | EC2 Instance, AMI, Key Pair, Lambda Function, Lambda Layer, Lambda Permission, Event Source Mapping, ECR Repository, ECR Lifecycle Policy, EKS Cluster, ECS Cluster |
| **Storage** (12) | S3 Bucket, EBS Volume, RDS Instance, DB Subnet Group, DB Parameter Group, Aurora Cluster, DynamoDB Table, SNS Topic, SNS Subscription, SQS Queue, SQS Queue Policy, SSM Parameter |
| **Identity** (7) | IAM Role, IAM Policy, IAM User, IAM Group, IAM Instance Profile, KMS Key, Secrets Manager Secret |
| **Monitoring** (3) | Log Group, Metric Alarm, Dashboard |

### Limitations

- **AWS only.** No GCP, Azure, or other cloud providers yet.
- **No cross-stack references.** One deployment cannot reference the outputs of another deployment yet.
- **No automatic rollback.** Failed deployments stop and report — they don't automatically revert completed resources. Operators can manually trigger a targeted rollback with `praxis delete Deployment/<key> --rollback`, which deletes only confirmed-provisioned resources in reverse dependency order.

See [FUTURE.md](docs/FUTURE.md) for what's coming next and [`examples/`](examples/) for ready-to-use templates.

---

## Documentation

| Document | Audience | Description |
| ---------- | ---------- | ------------- |
| [Index](docs/INDEX.md) | Everyone | One-table directory of all documentation |
| [Architecture](docs/PRAXIS_ARCHITECTURE.md) | Everyone | How Praxis works — Restate-powered core, modular drivers, design tradeoffs |
| [Codebase](docs/CODEBASE.md) | Contributors | Directory map, binaries, key files, entry points for common tasks |
| [Glossary](docs/GLOSSARY.md) | Everyone | A–Z definitions of Praxis terms |
| [Drivers](docs/DRIVERS.md) | Contributors | Driver model, contract, state management, reconciliation, building new drivers |
| [Orchestrator](docs/ORCHESTRATOR.md) | Contributors | Deployment workflows, DAG scheduling, state lifecycle, delete flow |
| [Templates](docs/TEMPLATES.md) | Platform Engineers | CUE template system, expression evaluation, registry, policy enforcement, data sources |
| [Auth & Workspaces](docs/AUTH.md) | Everyone | Credential management, workspace isolation, account selection |
| [CLI Reference](docs/CLI.md) | Users | All commands, output formats, exit codes, timeouts |
| [API Reference](docs/API.md) | Integrators / Agents | The HTTP API — every operation as a Restate ingress call, OpenAPI spec |
| [Operator Guide](docs/OPERATORS.md) | Operators | Deployment, configuration, registration, monitoring, troubleshooting |
| [Error Handling](docs/ERRORS.md) | Contributors | Error classification, status codes, error codes |
| [Events](docs/EVENTS.md) | Contributors | CloudEvents pipeline, event types, webhook sinks, retention |
| [Developer Guide](docs/DEVELOPERS.md) | Contributors | Building, testing, project structure, contributing |
| [Extending Praxis](docs/EXTENDING.md) | Contributors | Build custom drivers in any language without forking — extension contract, Python example, deployment patterns |

---

## Contributing

Praxis is Apache 2.0 licensed. See [LICENSE](LICENSE).

If you are interested in becoming a contributor, contact me via email.

See [docs/DEVELOPERS.md](docs/DEVELOPERS.md) for building, testing.

## License

Licensed under the Apache License, Version 2.0.
