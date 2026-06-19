# Praxis HTTP API

> Everything the CLI does is an HTTP call to the Restate ingress. This document
> is the integration reference for scripts, CI/CD pipelines, and AI agents.
> A machine-readable spec lives at [`api/openapi.yaml`](../api/openapi.yaml).

---

## Conventions

The Restate ingress (default `http://localhost:8080`) routes requests to
Praxis services using three URL shapes:

| Service type | URL shape | Example |
|--------------|-----------|---------|
| Service (stateless) | `POST /{Service}/{Handler}` | `POST /PraxisCommandService/Apply` |
| Virtual Object | `POST /{Object}/{key}/{Handler}` | `POST /DeploymentStateObj/my-app/GetDetail` |
| Workflow | managed internally | (submitted by the command service) |

- All bodies are `application/json`. Handlers that take no input accept an empty body.
- **Idempotency:** pass an `idempotency-key` header to make any call exactly-once
  across retries. The command service additionally derives idempotent workflow
  submissions from the deployment key.
- **Errors** are returned with meaningful HTTP status codes: 400 (validation),
  404 (not found), 409 (conflict), 500 (internal). Error strings embed stable
  error-code tokens (`VALIDATION_ERROR`, `NOT_FOUND`, `CONFLICT`, `TEMPLATE_INVALID`,
  `GRAPH_INVALID`, `PROVISION_FAILED`, `DELETE_FAILED`, `INTERNAL_ERROR`, `AUTH_*`)
  defined in `pkg/types/errorcode.go`.

The Go types referenced below all live in [`pkg/types/contract.go`](../pkg/types/contract.go)
— that file is the source of truth for request/response shapes.

## Command Service (`PraxisCommandService`)

The front door for all write operations. `POST /PraxisCommandService/{Handler}`:

| Handler | Request → Response | Purpose |
|---------|--------------------|---------|
| `Apply` | `ApplyRequest` → `ApplyResponse` | Evaluate inline CUE (or a template ref) and start an async deployment |
| `Plan` | `PlanRequest` → `PlanResponse` | Dry-run: evaluate + per-field diffs, no changes dispatched |
| `Deploy` | `DeployRequest` → `DeployResponse` | Deploy a **registered** template by name (production path) |
| `PlanDeploy` | `PlanDeployRequest` → `PlanDeployResponse` | Dry-run for a registered template |
| `ApplySavedPlan` | `ApplySavedPlanRequest` → `DeployResponse` | Execute a previously saved execution plan without re-evaluating |
| `DeleteDeployment` | `DeleteDeploymentRequest` → `DeleteDeploymentResponse` | Async teardown in reverse dependency order |
| `RollbackDeployment` | `DeleteDeploymentRequest` → `DeleteDeploymentResponse` | Delete only resources proven ready by the event store |
| `Import` | `ImportRequest` → `ImportResponse` | Adopt an existing cloud resource |
| `Approve` | `ApprovalRequest` → `ApprovalResponse` | Resume a deployment suspended at an approval gate |
| `RollbackTo` | `RollbackToRequest` → `DeployResponse` | Revert a deployment to a previous known-good generation |
| `Reject` | `ApprovalRequest` → `ApprovalResponse` | Reject a suspended deployment (finalizes as Cancelled) |
| `RegisterTemplate` | `RegisterTemplateRequest` → `RegisterTemplateResponse` | Register/update a template (previous version kept for rollback) |
| `GetTemplate` | `string` → `TemplateRecord` | Fetch a registered template |
| `ListTemplates` | — → `[]TemplateSummary` | List registered templates |
| `DeleteTemplate` | `DeleteTemplateRequest` → — | Remove a template |
| `ValidateTemplate` | `ValidateTemplateRequest` → `ValidateTemplateResponse` | Validate CUE source without planning |
| `AddPolicy` / `RemovePolicy` / `ListPolicies` / `GetPolicy` | see `pkg/types` | Manage CUE policies (global or per-template) |

Apply/Deploy/Delete are **asynchronous**: the response confirms acceptance and
returns a `deploymentKey`; poll `DeploymentStateObj/{key}/GetDetail` for progress.

### Example: deploy and poll

```bash
# Submit
curl -s -X POST http://localhost:8080/PraxisCommandService/Deploy \
  -H 'content-type: application/json' \
  -d '{"template": "webapp", "account": "prod", "variables": {"env": "staging"}, "deploymentKey": "my-webapp"}'

# Poll until status is Complete / Failed
curl -s -X POST http://localhost:8080/DeploymentStateObj/my-webapp/GetDetail \
  -H 'content-type: application/json' -d 'null'
```

### Example: drive an approval gate programmatically

Deployments into a protected workspace stop in `AwaitingApproval`. CI systems
or agents approve them with one call — this is the HTTP equivalent of
`praxis approve`:

```bash
# Deployment is parked: GetDetail shows "status": "AwaitingApproval"
curl -s -X POST http://localhost:8080/PraxisCommandService/Approve \
  -H 'content-type: application/json' \
  -d '{"deploymentKey": "my-webapp", "decidedBy": "release-bot", "comment": "CAB-1402"}'

# Rejection terminates it as Cancelled without dispatching anything
curl -s -X POST http://localhost:8080/PraxisCommandService/Reject \
  -H 'content-type: application/json' \
  -d '{"deploymentKey": "my-webapp", "decidedBy": "release-bot", "comment": "freeze window"}'
```

Both decisions are appended to the deployment's event stream
(`dev.praxis.deployment.approval.approved` / `.rejected`) with the supplied
identity and comment.

### Example: point-in-time rollback

```bash
# List the deployment's generations (rollback targets)
curl -s -X POST http://localhost:8080/DeploymentStateObj/my-webapp/ListGenerations \
  -H 'content-type: application/json' -d 'null'

# Roll back to generation 1 (must have finished Complete)
curl -s -X POST http://localhost:8080/PraxisCommandService/RollbackTo \
  -H 'content-type: application/json' \
  -d '{"deploymentKey": "my-webapp", "toGeneration": 1}'
```

## Read Model (Virtual Objects)

| Endpoint | Request → Response | Purpose |
|----------|--------------------|---------|
| `POST /DeploymentStateObj/{key}/GetDetail` | — → `DeploymentDetail` | Full deployment status, per-resource states, outputs, errors |
| `POST /DeploymentStateObj/{key}/RequestCancel` | — → — | Cooperative cancel of a running deployment |
| `POST /DeploymentIndex/global/List` | `ListFilter` → `[]DeploymentSummary` | All deployments (optional workspace filter) |
| `POST /ResourceIndex/global/Query` | `{kind, workspace}` → entries | Cross-deployment resource listing by Kind |
| `POST /DeploymentEventStore/{key}/ListSince` | `int64` → `[]SequencedCloudEvent` | Event stream cursor read (`0` = everything) |
| `POST /DeploymentEventStore/{key}/ListByType` | `string` → `[]SequencedCloudEvent` | Events filtered by type prefix |
| `POST /DeploymentEventStore/{key}/Count` | — → `int64` | Live event count |

`praxis observe` is a poll loop over `ListSince` — agents can do the same.

## Drivers (Virtual Objects, one per resource)

Each resource kind is a Virtual Object service named after the kind
(`S3Bucket`, `VPC`, `SecurityGroup`, …) keyed by the resource key
(key shapes are documented in [DRIVERS.md](DRIVERS.md)):

| Endpoint | Purpose |
|----------|---------|
| `POST /{Kind}/{key}/GetStatus` | Resource status + last error |
| `POST /{Kind}/{key}/GetOutputs` | Resource outputs (ARN, IDs, …) |
| `POST /{Kind}/{key}/Reconcile` | Trigger an immediate drift check |

Provision/Delete/Import on drivers are normally dispatched by the orchestrator —
prefer the command service for writes.

## Workspaces, Sinks, Retention

| Endpoint | Purpose |
|----------|---------|
| `POST /WorkspaceService/{name}/Configure` | Create/update a workspace |
| `POST /WorkspaceService/{name}/Get` | Read workspace config |
| `POST /WorkspaceService/{name}/SetEventRetention` / `GetEventRetention` | Event retention policy |
| `POST /WorkspaceIndex/global/List` | List workspace names |
| `POST /NotificationSinkConfig/global/Upsert` | Register a webhook / restate_rpc sink |
| `POST /NotificationSinkConfig/global/List` / `Get` / `Remove` / `Health` | Manage sinks |
| `POST /SinkRouter/Test` | Send a synthetic test event to a named sink |

## Schema discovery

Resource spec shapes are CUE schemas under [`schemas/aws/`](../schemas/aws/).
Offline access without the API:

```bash
praxis list schemas              # every kind + schema file
praxis get schema S3Bucket       # full CUE schema for one kind
praxis get schema S3Bucket -o json
```

## CLI parity

Every CLI command is a thin wrapper over these endpoints
(see [`internal/cli/client.go`](../internal/cli/client.go) for the exact mapping)
and supports `-o json` for machine-readable output. CLI exit codes are stable:
`0` success, `1` general, `3` not found, `4` validation, `5` conflict, `6` auth —
see [CLI.md](CLI.md).
