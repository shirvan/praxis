# Operators Guide

This guide is for platform engineers who deploy, configure, and maintain a Praxis stack.

## Deployment Model

Praxis consists of three service tiers, all fronted by a Restate server:

| Component          | Description                                          | Scaling                                    |
|--------------------|------------------------------------------------------|--------------------------------------------|
| **Restate Server** | Durable execution engine — state, journals, timers   | Single instance (or HA cluster)            |
| **Praxis Core**    | Command service, template engine, orchestrator       | Stateless; scale horizontally              |
| **Driver Services**| One binary per resource type (S3, SecurityGroup, EC2)| Stateless; scale horizontally per type     |

Every component is shipped as a Docker image. The reference topology is captured in [docker-compose.yaml](../docker-compose.yaml).

### Port Map (Reference Stack)

| Service      | Container Port | Host Port | Purpose              |
|--------------|---------------|-----------|----------------------|
| Restate      | 8080          | 8080      | Ingress (CLI + API)  |
| Restate      | 9070          | 9070      | Admin API            |
| Restate      | 9071          | 9071      | Metrics              |
| Praxis Core  | 9080          | 9083      | Restate endpoint     |
| S3 Driver    | 9080          | 9081      | Restate endpoint     |
| SG Driver    | 9080          | 9082      | Restate endpoint     |
| EC2 Driver   | 9080          | 9084      | Restate endpoint     |
| LocalStack   | 4566          | 4566      | Mock AWS (local dev) |

## Quick Start (Local Development)

### Prerequisites

- Docker + Docker Compose
- [just](https://github.com/casey/just) task runner
- Go >= 1.25 (building from source)

### Start the Stack

```bash
# Start everything — LocalStack, Restate, Core, drivers
just up

# The recipe:
#   1. Validates .env exists
#   2. Builds and starts all containers
#   3. Waits for LocalStack + Restate health checks
#   4. Registers all Praxis endpoints with Restate

# Stop and clean up (removes volumes)
just down
```

### Just Recipes

```bash
just              # List all available recipes
just up           # Start the full stack
just down         # Stop and remove volumes
just restart      # Rebuild and restart core + drivers, then re-register
just wait-stack   # Wait for LocalStack + Restate readiness
just status       # Show current container status
just doctor       # Fast endpoint + registration sanity check
```

**Logs:**

```bash
just logs         # Follow Praxis Core logs
just logs-s3      # Follow S3 driver logs
just logs-sg      # Follow SG driver logs
just logs-ec2     # Follow EC2 driver logs
just logs-drivers # Follow all driver logs together
just logs-all     # Follow all service logs
```

**Helpers:**

```bash
just ls-s3        # List S3 buckets in LocalStack
just rs-services  # List registered Restate services
just rs-deployments # List registered Restate deployments
```

## Configuration

Praxis Core and every driver load the same `.env` file. Copy `.env.example` to `.env` next to `docker-compose.yaml` before starting.

### Runtime Settings

| Variable                  | Default          | Description                               |
|---------------------------|------------------|-------------------------------------------|
| `PRAXIS_LISTEN_ADDR`      | `0.0.0.0:9080`  | HTTP listen address for Restate SDK       |
| `PRAXIS_RESTATE_ENDPOINT` | `http://localhost:8080` | Restate ingress URL (Core + CLI)   |
| `PRAXIS_SCHEMA_DIR`       | `./schemas`      | Filesystem path to the CUE schema bundle  |
| `AWS_ENDPOINT_URL`        | *(empty)*        | AWS endpoint override (e.g. `http://localstack:4566`) |

### Account Settings

| Variable                            | Required          | Description                                          |
|-------------------------------------|-------------------|------------------------------------------------------|
| `PRAXIS_ACCOUNT_NAME`               | Yes               | Account name users pass as `--account`               |
| `PRAXIS_ACCOUNT_REGION`             | Yes               | Default AWS region for this account                  |
| `PRAXIS_ACCOUNT_CREDENTIAL_SOURCE`  | Yes               | `static`, `role`, or `default`                       |
| `PRAXIS_ACCOUNT_ACCESS_KEY_ID`      | For `static`      | Access key for static credentials                    |
| `PRAXIS_ACCOUNT_SECRET_ACCESS_KEY`  | For `static`      | Secret key for static credentials                    |
| `PRAXIS_ACCOUNT_ROLE_ARN`           | For `role`        | Role ARN Praxis should assume                        |
| `PRAXIS_ACCOUNT_EXTERNAL_ID`        | Optional          | External ID for role assumption                      |

Praxis 0.1.0 supports exactly one configured account per deployed stack. Users select the account by name via `--account` or `PRAXIS_ACCOUNT`.

**Credential sources:**

- **static** — Explicit access key + secret key. Set both `PRAXIS_ACCOUNT_ACCESS_KEY_ID` and `PRAXIS_ACCOUNT_SECRET_ACCESS_KEY`.
- **role** — Assume `PRAXIS_ACCOUNT_ROLE_ARN` using the container's identity. Optionally set `PRAXIS_ACCOUNT_EXTERNAL_ID`.
- **default** — Use the standard AWS credential chain (instance profile, environment, config file).

## Restate Administration

### Register Endpoints

Each Praxis service must be registered with Restate before it can receive invocations. The `just register` recipe handles this automatically:

```bash
just register
```

For manual registration or debugging:

```bash
# Register S3 driver
curl -X POST http://localhost:9070/deployments \
  -H 'content-type: application/json' \
  -d '{"uri": "http://praxis-s3:9080"}'

# Register SG driver
curl -X POST http://localhost:9070/deployments \
  -H 'content-type: application/json' \
  -d '{"uri": "http://praxis-sg:9080"}'

# Register EC2 driver
curl -X POST http://localhost:9070/deployments \
  -H 'content-type: application/json' \
  -d '{"uri": "http://praxis-ec2:9080"}'

# Register Praxis Core
curl -X POST http://localhost:9070/deployments \
  -H 'content-type: application/json' \
  -d '{"uri": "http://praxis-core:9080"}'
```

### Verify Registration

```bash
# List registered services
curl http://localhost:9070/services | jq '.services[].name'

# List deployments
curl http://localhost:9070/deployments | jq .
```

## Monitoring

### Health Checks

| Endpoint                                    | Checks             |
|---------------------------------------------|---------------------|
| `GET http://localhost:9070/health`           | Restate server      |
| `GET http://localhost:4566/_localstack/health` | LocalStack (dev)  |

For Praxis services, verify registration via `just rs-services` or `just doctor`.

### Observability

- **Restate metrics**: Exposed on port 9071. Scrape with Prometheus or your preferred tool.
- **Structured logs**: All Praxis services emit JSON logs via Go's `slog` package. Collect with your log aggregation tool.
- **Deployment events**: Use `praxis observe Deployment/<key>` to stream real-time progress.

## Resource Lifecycle

Every managed resource follows this state machine:

```
Pending → Provisioning → Ready ⟲ (reconcile every 5 min)
                           ↓
                         Error ← external drift / failures
                           ↓
Ready → Deleting → Deleted
```

### Status Meanings

| Status         | Description                                             |
|----------------|---------------------------------------------------------|
| `Pending`      | Declared but not yet provisioned                        |
| `Provisioning` | Provision handler executing                             |
| `Ready`        | Resource exists and matches desired state               |
| `Error`        | Something went wrong — check the error field            |
| `Deleting`     | Delete handler executing                                |
| `Deleted`      | Resource removed (tombstone for audit trail)            |

### Modes

| Mode       | Behavior                                                    |
|------------|-------------------------------------------------------------|
| `Managed`  | Full lifecycle: provision, reconcile, correct drift, delete |
| `Observed` | Import-only: detect drift but never modify the resource     |

## Reconciliation

Drivers reconcile automatically on a **5-minute interval** using Restate durable timers. During each cycle:

1. The driver reads actual cloud state via the provider API
2. Compares against the desired spec stored in the Virtual Object
3. **Managed mode**: Corrects any drift by re-applying the configuration
4. **Observed mode**: Reports drift without correcting

The reconcile loop survives process restarts — Restate's durable timers ensure the next cycle fires even if the driver container is replaced.

### External Deletion

If a resource is deleted outside Praxis, the driver transitions to `Error` status with a descriptive message. It does **not** re-provision automatically — an operator must explicitly re-apply to recreate the resource.

## Troubleshooting

### Unknown account

```
unknown account "production"
```

Verify `PRAXIS_ACCOUNT_NAME` in `.env` matches the name users pass with `--account` or in their template's `variables.account`.

### Credentials valid but AWS calls fail

Check that the credential source matches the environment:

- **static**: Both `PRAXIS_ACCOUNT_ACCESS_KEY_ID` and `PRAXIS_ACCOUNT_SECRET_ACCESS_KEY` are set
- **role**: The container identity can assume `PRAXIS_ACCOUNT_ROLE_ARN`
- **default**: A working AWS credential chain is available (instance profile, env vars, config)

### Driver not registered

```bash
# Re-register all endpoints
just register

# Verify
just rs-services
```

### Provision returns 409

The resource already exists but is owned by another AWS account or was created outside Praxis. Use `praxis import` instead, or confirm the selected Praxis account matches the target AWS identity.

### Delete returns 409

The resource is not empty (e.g., S3 bucket with objects). Empty it manually, then retry the delete.

### Reconcile shows Error status

Check the error field with `praxis get <Kind>/<Key>`. Common causes:

- IAM permissions insufficient for the operation
- Resource deleted externally (re-apply to recreate)
- AWS API throttling (will auto-retry via Restate)

### Stack fails to start

```bash
# Check container status
just status

# Check health endpoints
just doctor

# View all logs
just logs-all
```
