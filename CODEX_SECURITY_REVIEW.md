# Security Review

## Remediation status — 2026-07-14

This file is a working review record and is intentionally not part of the public
repository history. The current patch set fixes the highest-impact application data
leak found by this review, but it does **not** close most of the broader security
hardening backlog.

| Finding | Status | Current boundary |
|---|---|---|
| EC2 KeyPair private key in Core state and events | **Fixed for normal Praxis data paths; journal exposure remains** | Durable driver state uses a type that cannot represent private key material. Core normalization, deployment state, `resource.ready` events, notification payloads, and expression hydration omit it. Restate still journals the initial AWS callback and durable handler response; importing a user-owned public key is the path that keeps private material out of Praxis entirely. |
| Unauthenticated Restate ingress trust boundary | **Open** | The risk is documented, but the Helm chart still supplies no default-deny/allowlist `NetworkPolicy` and Praxis has no caller authorization layer. Reachability to ingress must still be treated as AWS-administrator-equivalent access. |
| Static credentials and durable secret persistence | **Open** | Static credentials can still be injected into Core and every driver pack. Credential responses, secret resource inputs, and resolved plan specs can still enter Restate or saved-plan storage. HMAC signing proves integrity, not confidentiality. |
| Webhook SSRF and secret-header exfiltration | **Open** | Delivery still uses the default HTTP transport/redirect behavior without destination allowlists, private-address rejection, redirect policy, or port restrictions. |
| Arbitrary AWS endpoint override | **Open** | `EndpointURL` remains accepted and applied as the AWS SDK base endpoint without a production-mode gate or allowlist. |
| Helm workload hardening | **Open** | Pod/container security contexts, service-account token controls, and restrictive network policy have not been added. |
| Supply-chain reproducibility | **Partially fixed; mostly open** | CI now pins the CUE generator to the version used by `go.mod`, preventing silent schema churn. Actions still use mutable major tags; Compose/Helm retain mutable image tags; SBOM/signature/vulnerability gates are not yet complete. |
| Lambda permission event-source-token masking | **Open** | `spec.eventSourceToken` is not declared in the adapter's `SensitiveFields`. |
| Unified sensitive-output policy | **Open** | KeyPair now has a resource-specific structural boundary, but there is no registry-wide public/sensitive-durable/ephemeral classification enforced at every serialization boundary. |
| Security verification backlog | **Partially started** | KeyPair now has unit, hydration, and Docker end-to-end marker-secret regressions. Network, SSRF, endpoint-policy, workload, supply-chain, backup, and all-resource marker-secret verification remain. |

Practical conclusion: the concrete KeyPair P0 is substantially contained, and the
correctness patch also reduces error-handling and state-integrity risk. Praxis is
still an experimental trusted-network system, not a hardened multi-tenant or
internet-exposed service. The next security implementation pass should prioritize
the ingress/network boundary, webhook egress policy, and a unified secret
classification/serialization guard.

## Threat-model baseline

`docs/AUTH.md:19` explicitly states that anyone reaching Restate ingress can read or
configure AWS credentials. This review accepts that documented model, but the shipped
deployment must enforce the boundary it depends on.

The most serious current issue is sensitive data crossing *beyond* its intended
one-time or trust-bounded location.

## P0 — EC2 private key becomes durable orchestration data

The KeyPair driver correctly attempts not to persist private material:

- it saves the one-time return value at
  `internal/drivers/keypair/driver.go:188`;
- replaces stored outputs with values derived from Describe at lines 189–195; and
- adds the private material back only to the Provision response at line 199.

The provider adapter reverses that protection. Its `NormalizeOutputs` adds
`privateKeyMaterial` at `internal/core/provider/keypair_adapter.go:81-90`.

The orchestrator then:

- stores all normalized outputs in deployment state at
  `internal/core/orchestrator/workflow.go:729-735`; and
- places the same output map into a `resource.ready` CloudEvent at lines 739–748.

`NewResourceReadyEvent` confirms the outputs are part of event data at
`internal/core/orchestrator/event_builders.go:558-572`, and the event store persists
the record in chunk state (`event_store.go:74-95`). Event fan-out can forward it to
notification sinks.

Impact: a one-time SSH private key can be retained in Restate deployment state,
journal/event storage, CLI/API reads, backups, and external webhooks.

Immediate fix:

1. Never include `PrivateKeyMaterial` in `NormalizeOutputs`.
2. Add output metadata that distinguishes public, sensitive-durable, and
   ephemeral-one-time values.
3. Reject ephemeral values from deployment state, CloudEvents, notifications,
   expression hydration, logs, and saved plans.
4. Prefer user-provided public keys. If generated private keys remain supported,
   deliver them through an explicit one-time secret channel with clear loss/recovery
   semantics.
5. Add an end-to-end test that searches serialized deployment state, event records,
   CLI JSON, and captured webhook bodies for a marker private key.

Rotation/response note: if any non-test deployment has created KeyPairs through the
orchestrator, assume the private key may exist in retained Restate data and event
sinks. Inventory those deployments and rotate affected keys after the code path is
fixed.

## P1 — ingress trust boundary is documented but not deployed defensively

The Helm chart contains no `NetworkPolicy`. Restate exposes ingress and admin ports
inside a ClusterIP service, and service handlers log that requests are accepted
without signature validation. A ClusterIP limits exposure outside the cluster, but
does not restrict other pods/namespaces.

Add, enabled by default:

- NetworkPolicy allowing ingress only from Praxis services and explicitly selected
  operator/CI namespaces;
- separate policies for Restate ingress, Restate admin, core, and driver endpoints;
- optional API-key/OIDC/mTLS protection for human/CI ingress;
- no public admin service; and
- egress policies allowing only required AWS endpoints, DNS, Restate, and approved
  webhooks.

Treat access to port 8080 as AWS-account administrator access unless finer-grained
authorization is added.

## P1 — static credentials and secret persistence

The chart can inject the same static AWS credentials into core and every driver pack
(`charts/praxis/templates/core-deployment.yaml:30-36` and
`driver-deployment.yaml:37-43`). Values documentation suggests passing the secret by
`--set`, which can expose it in shell history and Helm release values. Kubernetes
Secret base64 encoding is not encryption.

Prefer:

- IRSA/EKS Pod Identity or the platform's workload-identity equivalent;
- separate least-privilege roles per driver pack and core;
- external secret stores for any unavoidable static material;
- encrypted Kubernetes secrets at rest; and
- no credential values in Helm values/history.

Praxis also persists credential responses and resource secret inputs in Restate
state/journals. Examples include AuthService cached credentials, Secrets Manager
`SecretString`, SSM parameter `Value`, and database master passwords. Saved execution
plans copy raw resolved resource specs at
`internal/core/command/saved_plan.go:37-48`; HMAC protects integrity, not
confidentiality.

Required controls:

- encrypt Restate volumes and backups;
- document retention and secure deletion;
- avoid storing secret plaintext where a hash/reference/version is sufficient;
- never include raw resolved secrets in user-exportable saved plans; and
- add automated serialized-state scans with marker secrets.

## P1 — webhook SSRF and credential exfiltration path

The notification CUE schema accepts any HTTPS URL
(`schemas/notifications/sink.cue:16-23`). The HTTP client at
`internal/core/orchestrator/notification_sinks.go:567-590` uses the default redirect
and dialing behavior. DNS resolution is not checked against loopback, link-local,
private, metadata, or cluster ranges. Resolved SSM header values are then attached to
the request.

An ingress-authorized caller can therefore configure a sink that targets internal
services through HTTPS/DNS, and can cause secret headers to be sent to it. The caller
already holds a strong trust position, but this still expands compromise into the
cluster/network and external secret exfiltration.

Mitigations:

- explicit hostname/domain allowlists per environment;
- resolve and reject loopback, private, link-local, multicast, metadata, and cluster
  service ranges for every connection;
- re-check after DNS resolution and on every redirect;
- disable redirects by default or allow only same-origin redirects;
- restrict allowed ports;
- prevent arbitrary sensitive headers unless the destination is approved; and
- use an egress proxy/NetworkPolicy as an independent control.

## P1 — arbitrary AWS endpoint override

Account configuration accepts `EndpointURL` and applies it as the AWS SDK base
endpoint (`internal/core/authservice/service.go:474-475` and
`client.go:95-96`). Signed requests and credentials can therefore be directed to an
operator-chosen endpoint.

Endpoint override is useful for Moto/local development, but production should disable
it or constrain it to an allowlist and development-mode flag. Configuration changes
should be authenticated and audited.

## P1 — Helm workload hardening is missing

Core, driver, and Restate pod specs do not set a pod/container `securityContext` and
do not disable service-account token mounting. The images may run non-root, but the
chart should enforce the property.

Recommended defaults:

```yaml
automountServiceAccountToken: false
securityContext:
  runAsNonRoot: true
  seccompProfile:
    type: RuntimeDefault
containerSecurityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities:
    drop: ["ALL"]
```

Add writable `emptyDir` mounts only where required, explicit service accounts for
workload identity, PodDisruptionBudgets, and backup/restore procedures for the
single-replica Restate StatefulSet.

## P1 — supply-chain reproducibility

Current mutable inputs include:

- GitHub Actions referenced by major version tags rather than commit SHA;
- `motoserver/moto:latest` and `amazon/aws-cli:latest` in Compose;
- Helm `global.imageTag: latest`;
- release publishing a `latest` image tag; and
- Restate 1.3 in Helm versus 1.6 in Compose.

Add:

- SHA-pinned actions with version comments and automated updates;
- digest/version-pinned test and runtime images;
- SBOM, provenance/attestation, signature, and vulnerability scanning;
- `govulncheck ./...` plus container/chart scanning in CI; and
- one supported Restate version or an explicit compatibility matrix.

## P2 — undeclared sensitive Lambda permission field

`LambdaPermissionSpec.EventSourceToken` exists at
`internal/drivers/lambdaperm/types.go:22` and is displayed in field diffs, but
`lambdaPermissionDescriptor` has no `SensitiveFields` declaration
(`internal/core/provider/lambdaperm_adapter.go:25-109`). Depending on the integration,
an event source token can be credential-like and should not appear in plan output,
saved specs, or `get --inputs`.

Declare `spec.eventSourceToken` sensitive and include it in the same registry-wide
sensitive-field guard used for database passwords, SSM values, and Secrets Manager
values.

## P2 — sensitive-output policy is too implicit

`SensitiveFields` currently protects selected **input diffs**. It does not define a
policy for outputs, durable state, events, saved plans, or notification payloads.

Add a single data-classification vocabulary:

| Class | Plan/CLI display | Durable state | Events/sinks | Expressions |
|---|---|---|---|---|
| Public | allowed | allowed | allowed | allowed |
| Sensitive durable | masked | encrypted/restricted | omitted/masked | explicit opt-in |
| Ephemeral secret | one-time only | forbidden | forbidden | forbidden |

Make serialization boundaries reject forbidden classes rather than relying on each
adapter to remember to omit them.

## Security verification backlog

- End-to-end marker-secret scan across plans, state, events, logs, CLI JSON, backups,
  and webhook payloads.
- NetworkPolicy tests in Kind: unauthorized pod denied; approved CLI/core/driver paths
  succeed.
- Pod-security admission test at restricted level.
- Webhook SSRF table covering redirects, DNS rebinding, IPv4/IPv6 private ranges,
  metadata endpoints, userinfo, unusual ports, and same-origin rules.
- EndpointURL production-mode rejection tests.
- Least-privilege IAM simulation per driver pack.
- Backup encryption and restore drill.
- Dependency, image, SBOM, and signature verification gates.
