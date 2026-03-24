# FUTURE.md — Planned Features & Roadmap

> Features are listed roughly in priority order.
>
> This file tracks gaps and extensions beyond the current implementation. Already-shipped pieces such as the Restate command service, DAG-driven deployment orchestrator, built-in CLI, deployment state/index objects, deployment events stream, observe flow, AWS SSM resolver, Auth Service (credential management), and Workspace Service (environment isolation) are intentionally omitted.

---

## Data Sources in Templates

Reference existing cloud resources directly in CUE templates without managing them.

**Technical approach:** Introduce a `data` block type alongside resource blocks in templates:

```cue
existingVpc: data.#Lookup & {
    kind:   "VPC"
    filter: {
        tag: Name: "production-vpc"
    }
    region: "us-east-1"
}

webServer: ec2.#EC2Instance & {
    spec: {
        vpcId: "${data.existingVpc.outputs.vpcId}"
    }
}
```

During template evaluation, Core dispatches lookup queries to the appropriate driver's `Describe` or `Find` handler. The driver queries AWS using the filter criteria and returns outputs without taking ownership. Results are injected as read-only outputs available for expression hydration. This differs from `import --observe` in that data sources are ephemeral per-evaluation — they don't create persistent driver state.

---

## Notification Sinks & External Event Delivery

Build external delivery and fan-out on top of the existing internal deployment event stream.

**Technical approach:** Praxis already records deployment events and exposes them to the CLI via polling. The remaining work is a lightweight event bus within Core that forwards those events to pluggable sinks and broadens the event catalog where needed:

- **Webhooks** — POST JSON payloads to user-configured endpoints (Slack, PagerDuty, custom)
- **Structured Logging** — JSON log lines to stdout for ingestion by existing log pipelines (Datadog, CloudWatch, ELK)
- **CLI streaming enhancements** — richer filtering, watch mode, and higher-fidelity event payloads for existing `praxis observe`

Events follow a common envelope: `{type, resource, timestamp, deployment, payload}`.

---

## AI Agent Concierge

A bring-your-own-API AI assistant that is Praxis-aware. The agent understands Praxis concepts (stacks, resources, drivers, templates, drift) and can query internal APIs to gather state, explain what happened, suggest fixes, and perform actions on the user's behalf.

**Supported providers:** Ollama (local), OpenAI API, Claude API. Users configure their preferred provider and API key; Praxis routes requests accordingly.

```mermaid
sequenceDiagram
    participant User
    participant Agent as Agent Service<br/>(Restate)
    participant LLM
    participant API as Praxis Core APIs

    User->>Agent: praxis ask "why did my deploy fail?"
    Agent->>LLM: Prompt + tool definitions
    LLM-->>Agent: Call getDeploymentStatus(key)
    Agent->>API: getDeploymentStatus
    API-->>Agent: Status + error details
    Agent->>LLM: Tool result
    LLM-->>Agent: Call getReconcileHistory(resource)
    Agent->>API: getReconcileHistory
    API-->>Agent: History entries
    Agent->>LLM: Tool result
    LLM-->>Agent: Final explanation
    Agent-->>User: "Deploy failed because sg-xyz was<br/>deleted externally. Re-apply to fix."
```

**Technical approach:** A Restate service that wraps LLM API calls in durable execution. The agent is given tool definitions that map to Praxis Core's internal APIs — `getDeploymentStatus`, `listResources`, `getReconcileHistory`, `getDrift`, `explainDAG`, etc. On each user prompt, the agent calls the LLM with the tool schema; the LLM decides which tools to invoke; results are fed back for a final response. Ollama runs locally (no network), OpenAI and Claude are external API calls wrapped in `restate.Run` for journaling. The agent can be exposed via the CLI (`praxis ask "why did my last deploy fail?"`).

---

## Historical Revisions & Audit Trail

Extend the current deployment state and per-deployment event feed into an immutable historical record across generations and re-applies.

**Technical approach:** Praxis already stores current deployment state, resource status, and append-only deployment events. The missing layer is durable revision history: preserve every apply/delete generation with template version, input values, resolved DAG, per-resource result, timestamps, and triggering identity. Query it via `praxis history <stack>` or the API, and support `praxis diff <deployment-a> <deployment-b>` for comparing historical revisions.

---

## Dependency Visualization

Render the resource dependency DAG as a visual graph so users can understand provisioning order and debug dependency issues.

**Technical approach:** Export the DAG (computed from expression parsing) as DOT, Mermaid, or JSON. `praxis plan --graph` outputs a Mermaid diagram. Nodes show resource kind + name; edges show which output feeds which input.

---

## Multi-Stack References

Allow one deployment to reference the outputs of another deployment. A "networking" stack produces a VPC ID; an "application" stack consumes it.

**Technical approach:** Introduce a cross-stack reference syntax, e.g. `${ stacks["networking"].outputs["vpc"].vpcId }`. Core resolves these by querying the referenced deployment's stored outputs via `GetOutputs` on the Deployment Workflow. Requires that the referenced stack is already deployed and in a `Complete` state. Creates an implicit dependency edge between stacks for ordering during coordinated applies.

---

## Rollbacks

Revert a deployment to a previous known-good state when provisioning fails or on user request.

```mermaid
flowchart TD
    subgraph Auto["Automatic (on failure)"]
        F["Provisioning fails"] --> L["Read ordered<br/>resource list"]
        L --> R1["Iterate in reverse<br/>calling Delete on each"]
    end

    subgraph Manual["User-initiated"]
        U["praxis rollback stack<br/>--to deployment-id"] --> D["Diff current state vs<br/>target deployment record"]
        D --> R2["Apply inverse changes"]
    end
```

**Technical approach:** The Deployment Orchestrator already maintains the ordered list of provisioned resources. On failure, rollback iterates that list in reverse and calls `Delete` on each resource. For user-initiated rollbacks (`praxis rollback <stack> --to <deployment-id>`), Core diffs the current state against the target deployment record and applies the inverse changes. Deployment History provides the state snapshots needed for this.

---

## Create-Before-Destroy Lifecycle

When immutable field changes require recreation, the orchestrator provisions the replacement before deleting the old resource.

This is intentionally deferred. Implementing it properly requires the driver contract to support provisioning a second instance with a temporary key, swapping references in dependent resources, and then tearing down the old instance — all under transactional semantics that the current driver model doesn't support. The coordination complexity (temporary naming, reference swapping, partial-failure recovery) is significantly higher than `preventDestroy` or `ignoreChanges` and isn't worth the investment until there are concrete use cases driving the need.

---

## Kubernetes Integration

A Kubernetes driver service that manages Deployments, Services, and Ingresses in a target cluster.

**Technical approach:** A standard Restate Virtual Object driver that wraps the Kubernetes client-go SDK. Allows Praxis to orchestrate both cloud infrastructure and application deployment from a single composition — e.g. a compound template that provisions an RDS database and deploys a Kubernetes workload that connects to it.

---

## Multi-Cloud

Support for GCP and Azure as additional cloud providers.

**Technical approach:** The driver service model already supports this — each cloud provider is a set of independent driver services with their own container images and schemas. A GCP provider ships drivers for GCS, Cloud SQL, Compute Engine, etc. An Azure provider ships drivers for Blob Storage, Azure SQL, VMs, etc. v1 is AWS-only; this extends the provider ecosystem to other clouds.

---

## Multi-Account

Manage resources across multiple AWS accounts (or multiple GCP projects / Azure subscriptions) from a single Praxis instance.

**Technical approach:** Credential configuration per driver instance via IAM role assumption (AWS), service account impersonation (GCP), or managed identity (Azure). Templates specify the target account/project as a parameter. Core passes the credential context to the driver, which assumes the appropriate role before making API calls. Enables hub-and-spoke patterns where a central platform team manages infrastructure across many workload accounts. The Auth Service already handles per-account credential resolution and STS AssumeRole — the remaining work is multi-account orchestration logic in Core and per-resource account overrides in templates.

---

## Additional Secret Backends

Extend beyond AWS SSM to support other secret stores.

**Technical approach:** Pluggable resolver interface behind the `ssm://` protocol. Add backends for AWS Secrets Manager, HashiCorp Vault, GCP Secret Manager, Azure Key Vault. Each backend implements a `Resolve(path) → value` interface. The URI scheme determines which backend handles the request (e.g. `vault:///secret/data/db-password`).

---

## Partial / Speculative Provisioning

Start provisioning long-running resources (e.g. RDS: 5–15 min) with a partial spec, then apply remaining fields as an in-place update after creation.

**Technical approach:** Extends the driver service contract with a two-phase model: `ProvisionPartial` (create with available fields) → await dependent outputs → `ProvisionUpdate` (apply remaining fields). Optimization for deployment speed; not needed until creation latency becomes a pain point.

---

## Central Rate Limit Advisor

Shared service that aggregates AWS API usage across all drivers and signals "slow down" when approaching account-level limits.

**Technical approach:** Drivers report API call counts to a central Virtual Object. The advisor tracks aggregate usage per API per account and returns throttle signals. Supplements per-driver rate limiting for high-scale deployments with many concurrent driver instances.
