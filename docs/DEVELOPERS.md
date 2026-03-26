# Developer Guide

This guide is for contributors developing the Praxis codebase. Adding features, writing drivers, fixing bugs, and running tests.

Praxis development benefits from scoping the imagination sandbox of the LLM Agents to pre-defined rules of Restate and Go, with heuristics of Praxis architecture. This off-loads much of the complex work to Restate while allowing for very flexible systems, planned and implemented by Agents.

## Prerequisites

- [Go](https://go.dev/) >= 1.25
- [Docker](https://www.docker.com/) + Docker Compose
- [just](https://github.com/casey/just) task runner
- [golangci-lint](https://golangci-lint.run/) for linting

## Directory Structure

```text
cmd/
  praxis/                      # CLI binary
  praxis-core/                 # Core command/orchestration service
  praxis-storage/              # Storage driver pack (S3, EBS, DBSubnetGroup, DBParameterGroup, RDSInstance, AuroraCluster)
  praxis-network/              # Network driver pack (SG, VPC, EIP, IGW, NACL, RouteTable, Subnet, NATGateway, VPCPeering, Route53Zone, Route53Record, Route53HealthCheck)
  praxis-compute/              # Compute driver pack (AMI, KeyPair, EC2, Lambda, LambdaLayer, LambdaPermission, EventSourceMapping)
  praxis-identity/             # Identity driver pack (IAMRole, IAMPolicy, IAMUser, IAMGroup, IAMInstanceProfile)

internal/
  cli/                         # CLI command implementations
    root.go                    # Root command, global flags
    apply.go, plan.go, ...     # Subcommand implementations
    client.go                  # Restate ingress client wrapper
    output.go                  # Table/JSON output formatters
  core/
    command/                   # Restate Basic Service (PraxisCommandService)
      service.go               # Service registration
      handlers_apply.go        # Apply handler
      handlers_plan.go         # Plan handler
      handlers_resource.go     # Delete + Import handlers
      handlers_template.go     # Template registry handlers
      handlers_policy.go       # Policy registry handlers
      pipeline.go              # Shared template evaluation pipeline
      datasource.go            # Data source validation, resolution, expression substitution
    config/                    # Configuration loading
    dag/                       # Dependency graph engine (pure Go)
      graph.go                 # DAG construction, cycle detection
      parser.go                # Expression reference extraction
      scheduler.go             # Topological sort + eager dispatch
    diff/                      # Plan diff engine
    orchestrator/              # Restate deployment workflows
      workflow.go              # DeploymentWorkflow (apply)
      delete_workflow.go       # DeploymentDeleteWorkflow
      deployment_state.go      # DeploymentState Virtual Object
      index.go                 # DeploymentIndex Virtual Object
      events.go                # DeploymentEvents Virtual Object
      hydrator.go              # Dispatch-time expression hydration
    provider/                  # Driver adapter registry
      registry.go              # Kind → service name mapping, Adapter interface (incl. Lookup)
      keys.go                  # Canonical resource key generation
      lookup_helpers.go        # Shared lookup utilities (tag merging, error classification)
      s3_adapter.go            # S3 adapter
      sg_adapter.go            # SG adapter
      ami_adapter.go           # AMI adapter
      igw_adapter.go           # IGW adapter
      ebs_adapter.go           # EBS adapter
      eip_adapter.go           # Elastic IP adapter
      ec2_adapter.go           # EC2 adapter
      keypair_adapter.go       # Key Pair adapter
      vpc_adapter.go           # VPC adapter
      nacl_adapter.go          # Network ACL adapter
      routetable_adapter.go    # Route Table adapter
      subnet_adapter.go        # Subnet adapter
      natgw_adapter.go         # NAT Gateway adapter
      vpcpeering_adapter.go    # VPC Peering adapter
      lambda_adapter.go        # Lambda Function adapter
      lambdalayer_adapter.go   # Lambda Layer adapter
      lambdaperm_adapter.go    # Lambda Permission adapter
      esm_adapter.go           # Event Source Mapping adapter
      iamrole_adapter.go       # IAM Role adapter
      iampolicy_adapter.go     # IAM Policy adapter
      iamuser_adapter.go       # IAM User adapter
      iamgroup_adapter.go      # IAM Group adapter
      iaminstanceprofile_adapter.go # IAM Instance Profile adapter
      route53zone_adapter.go   # Route 53 Hosted Zone adapter
      route53record_adapter.go # Route 53 DNS Record adapter
      route53healthcheck_adapter.go # Route 53 Health Check adapter
      rdsinstance_adapter.go   # RDS Instance adapter
      dbsubnetgroup_adapter.go # DB Subnet Group adapter
      dbparametergroup_adapter.go # DB Parameter Group adapter
      auroracluster_adapter.go # Aurora Cluster adapter
      targetgroup_adapter.go   # Target Group adapter
    registry/                  # Template + policy registries
      template_registry.go     # Restate VO for template storage
      policy_registry.go       # Restate VO for policy storage
      template_index.go        # Global template index
    resolver/                  # Secret resolution
      ssm.go                   # SSM Parameter Store resolver
      restate_ssm.go           # Restate-journaled SSM wrapper
    template/                  # Template engine
      engine.go                # CUE evaluation pipeline (extracts resources + data sources)
      types.go                 # DataSourceSpec, DataSourceFilter, EvaluationResult
  drivers/                     # Resource driver implementations
    contract.go                # Driver service contract docs
    state.go                   # Shared constants (StateKey, ReconcileInterval)
    s3/                        # S3 bucket driver
    sg/                        # Security Group driver
    ec2/                       # EC2 instance driver
    vpc/                       # VPC driver
    eip/                       # Elastic IP driver
    igw/                       # Internet Gateway driver
    ami/                       # AMI driver
    ebs/                       # EBS volume driver
    keypair/                   # Key Pair driver
    nacl/                      # Network ACL driver
    routetable/                # Route Table driver
    subnet/                    # Subnet driver
    natgw/                     # NAT Gateway driver
    vpcpeering/                # VPC Peering Connection driver
    lambda/                    # Lambda Function driver
    lambdalayer/               # Lambda Layer driver
    lambdaperm/                # Lambda Permission driver
    esm/                       # Event Source Mapping driver
    iamrole/                   # IAM Role driver
    iampolicy/                 # IAM Policy driver
    iamuser/                   # IAM User driver
    iamgroup/                  # IAM Group driver
    iaminstanceprofile/        # IAM Instance Profile driver
    route53zone/               # Route 53 Hosted Zone driver
    route53record/             # Route 53 DNS Record driver
    route53healthcheck/        # Route 53 Health Check driver
    rdsinstance/               # RDS Instance driver
    dbsubnetgroup/             # DB Subnet Group driver
    dbparametergroup/          # DB Parameter Group driver
    auroracluster/             # Aurora Cluster driver
    targetgroup/               # Target Group driver
  infra/
    awsclient/                 # Shared AWS client setup
    ratelimit/                 # Token bucket rate limiter

pkg/types/                     # Public shared types

schemas/aws/                   # CUE schemas per provider/service
  s3/s3.cue
  ec2/ec2.cue
  ec2/sg.cue
  ec2/ami.cue
  ec2/eip.cue
  ec2/keypair.cue
  ebs/ebs.cue
  igw/igw.cue
  nacl/nacl.cue
  routetable/routetable.cue
  subnet/subnet.cue
  natgw/natgw.cue
  vpc/vpc.cue
  vpcpeering/vpcpeering.cue
  lambda/function.cue
  lambda/layer.cue
  lambda/permission.cue
  lambda/event_source_mapping.cue
  iam/role.cue
  iam/policy.cue
  iam/user.cue
  iam/group.cue
  iam/instance_profile.cue
  route53/hosted_zone.cue
  route53/record.cue
  route53/health_check.cue
  rds/instance.cue
  rds/aurora_cluster.cue
  rds/parameter_group.cue
  rds/subnet_group.cue
  elb/target_group.cue

schemas/data/                  # Data source schemas (provider-agnostic)
  lookup.cue                   # #Lookup definition for data block entries

tests/integration/             # Integration tests (Testcontainers)
```

## Building

```bash
# Build everything: CLI + Core + drivers
just build

# Build the CLI only
just build-cli

# Build Core only
just build-core

# Build Docker images
just docker-build
```

## Testing

### Running Tests

```bash
# Unit tests (no Docker needed)
just test

# Scoped unit tests
just test-core       # Command service + DAG + orchestrator
just test-cli        # CLI
just test-s3         # S3 driver
just test-sg         # SG driver
just test-vpc        # VPC driver
just test-ami        # AMI driver
just test-ebs        # EBS driver
just test-eip        # EIP driver
just test-template   # Template engine + resolver
just test-igw        # IGW driver
just test-nacl       # NACL driver
just test-routetable # Route Table driver
just test-subnet     # Subnet driver
just test-natgw      # NAT Gateway driver
just test-vpcpeering # VPC Peering driver
just test-keypair    # Key Pair driver
just test-lambda     # Lambda Function driver
just test-lambdalayer # Lambda Layer driver
just test-lambdaperm # Lambda Permission driver
just test-esm        # Event Source Mapping driver
just test-iamrole    # IAM Role driver
just test-iampolicy  # IAM Policy driver
just test-iamuser    # IAM User driver
just test-iamgroup   # IAM Group driver
just test-iaminstanceprofile # IAM Instance Profile driver
just test-iam        # All IAM drivers
just test-route53    # All Route 53 drivers
just test-route53zone # Route 53 Hosted Zone driver
just test-route53record # Route 53 DNS Record driver
just test-route53healthcheck # Route 53 Health Check driver
just test-rds        # All RDS drivers

# Lint
just lint

# Integration tests (requires Docker — Testcontainers)
just test-integration

# Full local CI (lint → unit → integration)
just ci
```

### Testing Strategy

```mermaid
graph TD
    subgraph L3["Layer 3: End-to-End Tests"]
        E2E["Full deployment lifecycle<br/>Testcontainers: Restate + LocalStack"]
    end

    subgraph L2["Layer 2: Driver Integration Tests"]
        DIT["Driver CRUD against LocalStack<br/>Testcontainers: LocalStack only"]
    end

    subgraph L1["Layer 1: Unit Tests (Pure Logic)"]
        UT["DAG, drift detection,<br/>templates, diff, adapters<br/>No Docker required"]
    end

    L3 ~~~ L2
    L2 ~~~ L1

    style L1 fill:#4CAF50,color:#fff
    style L2 fill:#FF9800,color:#fff
    style L3 fill:#F44336,color:#fff
```

#### Layer 1: Unit Tests (Pure Logic)

No AWS or Restate required. Tests the most complex logic in isolation:

- DAG construction, cycle detection, topological ordering
- Expression reference parsing, typed hydration
- Drift detection per driver (pure function comparison)
- Template evaluation, schema validation
- Plan diff rendering
- Provider adapter registry, key generation

#### Layer 2: Driver Integration Tests

Driver CRUD operations tested against LocalStack via Testcontainers. No real AWS required.

Per driver:

- Provision (create, idempotency, update)
- Import (managed and observed modes)
- Delete (resource removed, tombstone preserved)
- Reconcile (no drift, managed drift correction, observed drift reporting)
- GetStatus / GetOutputs
- Error handling (terminal vs retryable)

#### Layer 3: End-to-End Tests

Full deployment lifecycle — template submission through the command service, orchestrator dispatching resources in dependency order, outputs propagating, delete in reverse order. Runs against Testcontainers (Restate + LocalStack).

## Driver Development

Each driver is a **Restate Virtual Object** managing the lifecycle of a single cloud resource type. See [DRIVERS.md](DRIVERS.md) for the full driver model documentation.

### File Layout

```text
internal/drivers/<kind>/
├── types.go       # Spec, Outputs, ObservedState, State structs
├── aws.go         # AWS SDK wrapper behind a testable interface
├── drift.go       # Pure-function drift detection
├── driver.go      # Restate Virtual Object with lifecycle handlers
├── driver_test.go # Unit tests
└── drift_test.go  # Drift detection tests

cmd/praxis-<pack>/
├── main.go        # Binds all drivers in this domain pack
└── Dockerfile     # Multi-stage distroless build

schemas/aws/<service>/<kind>.cue  # CUE schema for user-facing spec
```

### Driver Contract

Every driver Virtual Object implements 6 handlers. See [DRIVERS.md](DRIVERS.md) for full signatures and semantics.

| Handler     | Type      | Purpose                                      |
|-------------|-----------|----------------------------------------------|
| `Provision` | Exclusive | Idempotent create-or-converge                |
| `Import`    | Exclusive | Adopt existing resource                      |
| `Delete`    | Exclusive | Remove the resource                          |
| `Reconcile` | Exclusive | Periodic drift detection + correction        |
| `GetStatus` | Shared    | Return lifecycle status, mode, generation    |
| `GetOutputs`| Shared    | Return resource outputs (ARN, endpoint, etc.)|

### Key Design Rules

**Error classification** — See [Errors](ERRORS.md) for the full error model: terminal vs retryable errors, HTTP status code conventions, the shared `awserr` classifier package, structured failure summaries, and error codes.

**Side effects** — Every AWS API call must be wrapped in `restate.Run()` to journal the result:

```go
observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
    return d.api.DescribeBucket(rc, name)
})
```

**State model** — All driver state is stored as a single atomic K/V entry under the key `"state"`. This prevents torn state if a handler crashes mid-execution.

**Idempotent provision** — Always: check if exists → create if absent → configure always.

**Reconcile deduplication** — Use a `ReconcileScheduled` boolean in state to prevent timer fan-out.

**Import baseline** — Captures observed state as both desired and observed, so the first reconcile sees no drift.

**Delete safety** — Non-empty resources fail terminally. Already-deleted resources succeed (idempotent). Delete never schedules a reconcile timer.

### AWS Wrapper Pattern

Wrap the AWS SDK behind an interface for testability:

```go
type S3API interface {
    HeadBucket(ctx context.Context, name string) error
    CreateBucket(ctx context.Context, name, region string) error
    ConfigureBucket(ctx context.Context, spec S3BucketSpec) error
    DescribeBucket(ctx context.Context, name string) (ObservedState, error)
    DeleteBucket(ctx context.Context, name string) error
}
```

Rate limiting is built into the AWS wrapper layer — drivers never touch the limiter directly.

### Adding a New Driver

```mermaid
flowchart TD
    A["1. Create driver package<br/>internal/drivers/kind/"] --> B["2. Bind to domain pack<br/>cmd/praxis-pack/main.go"]
    B --> C["3. Create CUE schema<br/>schemas/aws/service/kind.cue"]
    C --> D["4. Add provider adapter<br/>internal/core/provider/"]
    D --> E["5. Register in adapter registry"]
    E --> F["6. Wire Docker + Justfile<br/>(if new pack)"]
    F --> G["7. Write unit + integration tests"]
```

1. Create `internal/drivers/<kind>/` with types, aws wrapper, drift detection, and driver
2. Add the driver to the appropriate domain pack entry point (e.g., add a VPC driver to `cmd/praxis-network/main.go` via an additional `.Bind()` call)
3. Create CUE schema in `schemas/aws/<service>/<kind>.cue`
4. Add provider adapter in `internal/core/provider/` (adapter + registry entry + key scope)
5. Update `docker-compose.yaml` registration if the driver pack is new
6. Add `just` recipes for the new driver's tests
7. Write unit tests (drift, spec synthesis) and integration tests

### Reference Implementations

Study the S3 driver (`internal/drivers/s3/`), Security Group driver (`internal/drivers/sg/`), EC2 driver (`internal/drivers/ec2/`), VPC driver (`internal/drivers/vpc/`), EIP driver (`internal/drivers/eip/`), AMI driver (`internal/drivers/ami/`), EBS driver (`internal/drivers/ebs/`), and KeyPair driver (`internal/drivers/keypair/`) — every pattern described here is demonstrated in those implementations.

The EC2 driver was built from [EC2_DRIVER_PLAN.md](ec2/EC2_DRIVER_PLAN.md), which documents the full process — CUE schema, types, AWS wrapper, drift detection, driver handlers, adapter, registry integration, Docker/Justfile wiring, and tests — with design rationale for each decision.

## Code Style

- **Logging**: Use `slog` structured logging throughout
- **Error handling**: Wrap errors with context using `fmt.Errorf("...: %w", err)`. See [Errors](ERRORS.md) for classification and status code conventions
- **Formatting**: `gofmt -s` (check with `just fmt-check`)
- **Linting**: `golangci-lint` (run with `just lint`)

## Release

Praxis uses [semver](https://semver.org/). Releases are **manual** — you control what version ships and with what notes. No automated release-on-push.

### Versioning

All services currently share a **single monolithic version**. After the initial public release, each service will move to independent per-service versioning.

| Component | Binary / Image | Current Versioning |
| --- | --- | --- |
| **CLI** | `praxis` | Shared tag |
| **Core** | `praxis-core` | Shared tag |
| **Network Pack** | `praxis-network` | Shared tag |
| **Compute Pack** | `praxis-compute` | Shared tag |
| **Storage Pack** | `praxis-storage` | Shared tag |
| **Identity Pack** | `praxis-identity` | Shared tag |
| **Monitoring Pack** | `praxis-monitoring` | Shared tag |
| **Notifications** | `praxis-notifications` | Shared tag |

### Release Workflow

```mermaid
sequenceDiagram
    participant Dev as Developer
    participant GH as GitHub Actions
    participant GHCR as ghcr.io
    participant Rel as GitHub Releases

    Dev->>Dev: just release-preflight <tag>
    Note over Dev: lint, test, build
    Dev->>GH: just release <tag> (tag + push)
    GH->>GH: lint, test, cross-compile CLI
    GH->>GHCR: Push Docker images
    GH->>Rel: Create draft release + tarballs
    Dev->>Rel: Edit notes and publish
```

```bash
# 1. Run pre-release checks (lint, test, build)
just release-preflight <tag>

# 2. Tag and push — triggers GitHub Actions to build artifacts and create a draft release
just release <tag>

# 3. Go to GitHub Releases, edit the draft, add release notes, and publish
```

### What Happens

1. `just release <tag>` validates the tag, checks for a clean working tree on `main`, creates an annotated git tag, and pushes it.
2. GitHub Actions ([`.github/workflows/release.yml`](../.github/workflows/release.yml)) runs: lint → test → cross-compile CLI (darwin/arm64, darwin/amd64, linux/amd64) → create a **draft** GitHub Release with tarballs and checksums attached.
3. Docker images for all services are built and pushed to `ghcr.io/shirvan/praxis-*`.
4. You edit the draft release on GitHub to add your release notes, then publish.

### Local-Only Build (No Tag)

```bash
# Build release artifacts locally without tagging (for inspection)
just release-build <tag>
```

This runs the `release-build` recipe which cross-compiles the CLI and service binaries into `dist/<tag>/`.

### CI

A separate CI workflow ([`.github/workflows/ci.yml`](../.github/workflows/ci.yml)) runs lint, format check, tests, and build on every push to `main` and on pull requests.

## License

Praxis is Apache 2.0 licensed. See [LICENSE](../LICENSE).
