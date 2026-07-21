# Praxis alpha

Praxis is a declarative AWS infrastructure control plane built with CUE and
Restate. This alpha release provides one uniform resource lifecycle across 51
AWS resource kinds, durable execution, planning, import, reconciliation,
rollback, conditions, events, a CLI and HTTP API, and downloadable artifacts
that do not require users to build Praxis from source.

> [!IMPORTANT]
> `alpha` is the only supported Praxis release, API contract, and image tag.
> It is mutable and may change incompatibly. Update the CLI, all six Praxis
> images, Helm chart, schemas, and templates together. Backwards compatibility
> between alpha revisions is not provided.

## Highlights

### One generic lifecycle for all 51 drivers

- All production drivers now use the same generic lifecycle kernel and the
  same handler surface for provisioning, import, deletion, reconciliation,
  status, inputs, outputs, and lookup.
- Resource state has one alpha shape. Old driver-local implementations and
  resource-local state structures were removed instead of retained as
  compatibility layers.
- Desired state, observed state, outputs, ownership mode, reconciliation
  policy, generation, errors, and conditions are persisted consistently.
- Late initialization is explicit: provider-selected defaults can be returned
  to the user without silently changing the declared contract.
- Pre-delete behavior is not exposed. Users remain responsible for preparing
  resources for deletion, as with Terraform.

### Predictable reconciliation and drift visibility

- Managed resources reconcile toward the user's declared state.
- `lifecycle.reconcile: "observe"` reports drift without performing provider
  writes. The resource remains Ready because it is satisfying the selected
  policy, while `DriftFree=False` and its reason make the divergence visible.
- Automatic reconciliation reports detection and correction and persists a
  `DriftFree=True` condition after provider state is restored.
- Resources deleted outside Praxis are reported as externally deleted and are
  not silently recreated by reconciliation. Provisioning is the explicit
  recovery operation.
- Imported observed resources remain externally owned. Praxis reports their
  drift and refuses to delete the provider resource.
- Conditions are available through both human-readable and JSON CLI output,
  together with reconciliation policy and ignored fields.

### Uniform lookup and data sources

- All 51 drivers participate in the generic lookup contract instead of
  maintaining parallel lookup implementations.
- Lookup supports canonical identifiers, native provider filters, stable
  normalization, and consistent not-found/conflict handling.
- Read-only data sources can resolve existing VPCs, subnets, security groups,
  S3 buckets, IAM roles, and Route 53 hosted zones into deployment inputs.

### Durable error and recovery behavior

- AWS error classification now happens inside `restate.Run()` callbacks.
  Validation, conflict, and not-found outcomes are terminal; throttling,
  network, and other transient failures remain retryable by Restate.
- A systemic error-classification issue was corrected across 17 drivers so
  terminal provider failures no longer enter durable retry loops.
- CLI wait behavior, deployment polling, rollback, and deletion paths were
  hardened to return structured terminal failures without hiding provider or
  resource context.
- Restate journals provider side effects and resumes workflows after process
  interruption without replaying already completed calls.

### Distribution without a source build

- Native CLI archives are provided for macOS, Linux, and Windows.
- The quick-start bundle runs the complete published stack with Docker Compose,
  Restate, and Moto without cloning the repository or compiling Praxis.
- The Helm chart installs Praxis Core, all five driver packs, and optionally a
  bundled Restate instance. It can also connect to an external Restate or
  Restate Cloud environment.
- All six Praxis images are published to GHCR under the single mutable `alpha`
  tag.
- Every downloadable artifact is covered by the attached SHA-256 checksum
  file and includes the Apache 2.0 license where applicable.

### Verification and documentation

- The production-topology acceptance suite compiles the real CLI, starts
  Praxis Core and all five production driver-pack processes, and verifies all
  51 driver services and 19 Core services through Restate.
- Provider-observable scenarios cover a cross-pack VPC → Subnet/Security Group
  → EC2 → S3 graph, output hydration, reverse deletion, observe-only drift,
  automatic correction, import, update, generation history, and rollback.
- A fast confidence lane covers high-signal deployment behavior, while the
  release lane executes the comprehensive Moto-backed integration suite.
- The product and documentation site now includes installation guidance,
  operating concepts, API and CLI references, a searchable 51-resource catalog
  with schemas and examples, and architecture diagrams.

## Downloadable artifacts

| Artifact | Purpose |
|---|---|
| `praxis_darwin_arm64.tar.gz` | Praxis CLI for Apple Silicon macOS |
| `praxis_darwin_amd64.tar.gz` | Praxis CLI for Intel macOS |
| `praxis_linux_arm64.tar.gz` | Praxis CLI for ARM64 Linux |
| `praxis_linux_amd64.tar.gz` | Praxis CLI for x86-64 Linux |
| `praxis_windows_amd64.zip` | Praxis CLI for x86-64 Windows |
| `praxis-alpha-quickstart.tar.gz` | No-clone Docker Compose evaluation stack |
| `praxis-alpha-chart.tgz` | Helm chart for Kubernetes deployment |
| `checksums.txt` | SHA-256 checksums for every artifact above |

Verify downloads before use:

```sh
shasum -a 256 -c checksums.txt
```

## Published container images

```text
ghcr.io/shirvan/praxis-core:alpha
ghcr.io/shirvan/praxis-storage:alpha
ghcr.io/shirvan/praxis-network:alpha
ghcr.io/shirvan/praxis-compute:alpha
ghcr.io/shirvan/praxis-identity:alpha
ghcr.io/shirvan/praxis-monitoring:alpha
```

## Quick start

Download the CLI archive for your platform, `checksums.txt`, and
`praxis-alpha-quickstart.tar.gz`, then:

```sh
tar -xzf praxis-alpha-quickstart.tar.gz
cd praxis-alpha-quickstart
./praxis-up
praxis version
praxis plan bucket.cue --account local
praxis deploy bucket.cue --account local --key quickstart --yes --wait
```

See the [installation guide](https://shirvan.github.io/praxis/docs/start/installation/),
[first deployment](https://shirvan.github.io/praxis/docs/start/first-deployment/),
and [AWS resource catalog](https://shirvan.github.io/praxis/resources/) for the
complete walkthrough.

## Alpha limitations

- Praxis has extensive unit, generic contract, Moto-backed integration, fault,
  and production-topology acceptance coverage, but this revision has not been
  validated against live AWS accounts. Moto cannot faithfully exercise every
  provider mutation; those cases remain explicit test limitations rather than
  being counted as successful AWS verification.
- The Restate ingress and admin endpoints are trusted administrative surfaces
  and must not be exposed to untrusted networks. Use private networking, an
  authenticating proxy, or Restate Cloud for remote access.
- Existing alpha state, plans, and templates may break when the mutable alpha
  contract advances.

Praxis is licensed under the
[Apache License 2.0](https://github.com/shirvan/praxis/blob/main/LICENSE).
