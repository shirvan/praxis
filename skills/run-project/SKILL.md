# Run Project

**Description**: Build, run, and test the Praxis project locally.

**When to Use**: Setting up a development environment, running the stack, or executing tests.

**Prerequisites**:
- Go 1.25+
- Docker & Docker Compose
- [just](https://github.com/casey/just) task runner
- CUE CLI (`cue` 0.16+)
- `jq` (for registration output)

---

## Quick Start

### 1. Clone & Install Dependencies

```bash
git clone <repo-url> && cd praxis
go mod download
```

### 2. Environment Setup

```bash
cp .env.example .env
# Edit .env with your AWS credentials or leave defaults for Moto (mock AWS)
```

### 3. Start the Full Stack

```bash
just up
```

This will:
1. Build all Docker images
2. Start Moto (mock AWS), Restate, and all Praxis services
3. Wait for health endpoints
4. Register all services with Restate

### 4. Verify

```bash
just status          # Check container health
curl localhost:9070/services | jq .  # List registered Restate services
```

---

## Common Just Recipes

| Recipe | Purpose |
|--------|---------|
| `just up` | Build + start + register everything |
| `just down` | Stop stack, remove volumes |
| `just restart` | Rebuild + restart Praxis services (not infra) |
| `just status` | Show container status |
| `just logs` | Follow praxis-core logs |
| `just logs-all` | Follow all service logs |
| `just logs-storage` | Follow storage driver pack logs |
| `just logs-network` | Follow network driver pack logs |
| `just logs-compute` | Follow compute driver pack logs |
| `just logs-identity` | Follow identity driver pack logs |
| `just logs-monitoring` | Follow monitoring driver pack logs |
| `just register` | Re-register all services with Restate |

---

## Building

```bash
just build           # Build everything → bin/
just build-cli       # CLI only → bin/praxis
just build-core      # Core only → bin/praxis-core
just docker-build    # Docker images only
```

Binaries output to `bin/`:
- `praxis` — CLI
- `praxis-core` — Orchestrator + command service + event bus / notification sinks
- `praxis-storage` — Storage driver pack (S3, EBS, RDS, SNS, SQS, etc.)
- `praxis-network` — Network driver pack (VPC, SG, Route53, ELB, etc.)
- `praxis-compute` — Compute driver pack (EC2, Lambda, ECR, etc.)
- `praxis-identity` — Identity driver pack (IAM)
- `praxis-monitoring` — Monitoring driver pack (CloudWatch)

---

## Testing

### Run All Unit Tests

```bash
just test
# equivalent to: go test ./internal/... ./pkg/... -v -count=1 -race -p 1
```

### Run Specific Driver Tests

```bash
just test-s3              # S3 bucket driver
just test-ec2             # EC2 instance driver
just test-iam             # All IAM drivers
just test-elb             # All ELB drivers (ALB, NLB, TG, Listener, Listener Rule)
just test-route53         # Route53 drivers
just test-lambda          # Lambda function driver
just test-rds             # RDS drivers
just test-sns             # SNS drivers
just test-sqs-all         # SQS drivers
just test-monitoring      # CloudWatch drivers
just test-ecr             # ECR drivers
just test-acm             # ACM certificate driver
```

### Run Core Tests

```bash
just test-core            # DAG, orchestrator, command service
just test-cli             # CLI command tests
```

### Run Integration Tests

Integration tests use Testcontainers (Docker required):

```bash
just test-integration          # Full suite
just test-core-integration     # Restate + Moto core lifecycle
just test-sqs-integration
just test-sqspolicy-integration
```

### Run Specific Test by Name

```bash
go test ./internal/drivers/s3/... -run TestProvisionBucket -v
```

---

## Using the CLI

After building:

```bash
# Deploy a template
bin/praxis deploy -f examples/s3/bucket.cue -v env=dev

# Check deployment status
bin/praxis get deployment my-deployment

# List all deployments
bin/praxis list deployments

# Watch events in real time
bin/praxis observe my-deployment

# Plan without deploying
bin/praxis plan -f examples/s3/bucket.cue -v env=dev

# Delete a deployment
bin/praxis delete my-deployment

# Inspect supported kinds and their schemas (offline, embedded CUE)
bin/praxis list schemas
bin/praxis get schema S3Bucket
```

All commands accept `-o json` for machine-readable output and return stable exit codes (0 success, 1 general, 2 timeout, 3 not found, 4 validation, 5 conflict, 6 auth).

---

## Service Ports

| Service | Port | Purpose |
|---------|------|---------|
| Restate ingress | 8080 | Handler invocations |
| Restate admin | 9070 | Service registration & management |
| Moto (mock AWS) | 4566 | Local AWS API mock |
| praxis-core | 9080 (host 9083) | Orchestrator + command service + event bus |
| praxis-storage | 9080 (host 9081) | Storage drivers |
| praxis-network | 9080 (host 9082) | Network drivers |
| praxis-compute | 9080 (host 9084) | Compute drivers |
| praxis-identity | 9080 (host 9085) | Identity drivers |
| praxis-monitoring | 9080 (host 9087) | Monitoring drivers |

---

## Troubleshooting

| Problem | Fix |
|---------|-----|
| `just up` fails on health check | Check Docker is running: `docker info` |
| Registration fails | Ensure Restate is healthy: `curl localhost:9070/health` |
| Build fails | Run `go mod download` then retry |
| Tests fail with port conflicts | Use `-p 1` flag or run `just test` (already serialized) |
| "Missing .env" error | Run `cp .env.example .env` |

## See Also

- [docs/DEVELOPERS.md](../../docs/DEVELOPERS.md) — Detailed dev guide
- [docs/OPERATORS.md](../../docs/OPERATORS.md) — Deployment & configuration
- [docs/CLI.md](../../docs/CLI.md) — CLI reference
