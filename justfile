# justfile — Praxis task runner
# Install: https://github.com/casey/just
# Usage: just <recipe>

set dotenv-load := true

# Default: show available recipes
default:
    @just --list

# Judy is a standalone Rust/Janet verification engine. These recipes are only
# a bridge into Judy's own toolchain; Judy remains independently buildable.
judy-check:
    @test -f judy/Cargo.toml || (echo "Judy is not initialized. Initialize the judy submodule first." && exit 1)
    just --justfile judy/justfile check

judy-test:
    @test -f judy/Cargo.toml || (echo "Judy is not initialized. Initialize the judy submodule first." && exit 1)
    just --justfile judy/justfile test

judy-ci:
    @test -f judy/Cargo.toml || (echo "Judy is not initialized. Initialize the judy submodule first." && exit 1)
    just --justfile judy/justfile ci

wait_timeout_seconds := "120"
test_heartbeat_seconds := "30"
# The serial integration suite starts isolated Restate testcontainers. Twenty
# minutes is too narrow on Docker Desktop even when every assertion is healthy.
integration_timeout := env_var_or_default("PRAXIS_INTEGRATION_TIMEOUT", "60m")

# ─── Development ────────────────────────────────────────────

# Start the full local stack and register every Praxis endpoint with
# Restate after the infra becomes reachable.
up:
    just ensure-env
    docker compose build --no-cache
    docker compose up -d
    just wait-stack
    just register
    @echo "✓ Stack is up. Restate admin: http://localhost:9070"

# Ensure the shared operator env file exists before local stack operations.
ensure-env:
    @test -f .env || (echo "Missing .env. Create it with: cp .env.example .env" && exit 1)

# Stop the local stack and remove volumes
down:
    docker compose down -v

# Wait until the infrastructure endpoints needed for registration are reachable.
# A real health loop is safer than a fixed sleep because image pulls and first
# boots can vary significantly across machines and CI environments.
wait-stack:
    #!/bin/sh
    timeout={{wait_timeout_seconds}}
    start=$(date +%s)
    echo "Waiting for Moto health endpoint (timeout: ${timeout}s)..."
    until curl -fsS http://localhost:4566/moto-api/ >/dev/null; do
        now=$(date +%s)
        elapsed=$((now-start))
        if [ "$elapsed" -ge "$timeout" ]; then
            echo "Timed out waiting for Moto after ${elapsed}s"
            exit 1
        fi
        printf "."
        sleep 1
    done
    echo

    start=$(date +%s)
    echo "Waiting for Restate admin health endpoint (timeout: ${timeout}s)..."
    until curl -fsS http://localhost:9070/health >/dev/null; do
        now=$(date +%s)
        elapsed=$((now-start))
        if [ "$elapsed" -ge "$timeout" ]; then
            echo "Timed out waiting for Restate after ${elapsed}s"
            exit 1
        fi
        printf "."
        sleep 1
    done
    echo

# Rebuild and restart the core + driver packs, then re-register them.
restart:
    just ensure-env
    docker compose build --no-cache praxis-core praxis-storage praxis-network praxis-compute praxis-identity praxis-monitoring
    docker compose up -d praxis-core praxis-storage praxis-network praxis-compute praxis-identity praxis-monitoring
    just wait-stack
    just register

# Show current container status for the full stack.
status:
    docker compose ps

# Follow logs for Praxis Core.
logs:
    docker compose logs -f praxis-core

# Follow logs for the storage driver pack (S3).
logs-storage:
    docker compose logs -f praxis-storage

# Follow logs for the network driver pack (SG, VPC, Route 53, ELB).
logs-network:
    docker compose logs -f praxis-network

# Follow logs for the compute driver pack (EC2).
logs-compute:
    docker compose logs -f praxis-compute

# Follow logs for the Identity driver pack.
logs-identity:
    docker compose logs -f praxis-identity

# Follow logs for the monitoring driver pack.
logs-monitoring:
    docker compose logs -f praxis-monitoring

# Follow logs for the notifications event service.
# Follow logs for all driver packs together.
logs-drivers:
    docker compose logs -f praxis-storage praxis-network praxis-compute praxis-identity praxis-monitoring

# Follow logs for all services
logs-all:
    docker compose logs -f

# Register all services with Restate (driver packs + core).
#
# The admin API registration step is intentionally kept explicit in the justfile
# instead of being hidden in a shell script. That keeps local troubleshooting
# easy and gives AI agents a single canonical place to learn the deployment URIs.
register:
    @echo "Registering storage driver pack with Restate..."
    @curl -fsS -o /dev/null -X POST http://localhost:9070/deployments \
        -H 'content-type: application/json' \
        -d '{"uri": "http://praxis-storage:9080", "force": true}'
    @echo "✓ Storage driver pack registered"
    @echo "Registering network driver pack with Restate..."
    @curl -fsS -o /dev/null -X POST http://localhost:9070/deployments \
        -H 'content-type: application/json' \
        -d '{"uri": "http://praxis-network:9080", "force": true}'
    @echo "✓ Network driver pack registered"
    @echo "Registering compute driver pack with Restate..."
    @curl -fsS -o /dev/null -X POST http://localhost:9070/deployments \
        -H 'content-type: application/json' \
        -d '{"uri": "http://praxis-compute:9080", "force": true}'
    @echo "✓ Compute driver pack registered"
    @echo "Registering Identity driver pack with Restate..."
    @curl -fsS -o /dev/null -X POST http://localhost:9070/deployments \
        -H 'content-type: application/json' \
        -d '{"uri": "http://praxis-identity:9080", "force": true}'
    @echo "✓ Identity driver pack registered"
    @echo "Registering monitoring driver pack with Restate..."
    @curl -fsS -o /dev/null -X POST http://localhost:9070/deployments \
        -H 'content-type: application/json' \
        -d '{"uri": "http://praxis-monitoring:9080", "force": true}'
    @echo "✓ Monitoring driver pack registered"
    @echo "Registering Praxis Core (command service + orchestrator)..."
    @curl -fsS -o /dev/null -X POST http://localhost:9070/deployments \
        -H 'content-type: application/json' \
        -d '{"uri": "http://praxis-core:9080", "force": true}'
    @echo "✓ Praxis core services registered"

# ─── Build ──────────────────────────────────────────────────

# Build everything: CLI, Core, and driver packs
build:
    go build -o bin/praxis ./cmd/praxis
    go build -o bin/praxis-core ./cmd/praxis-core
    go build -o bin/praxis-storage ./cmd/praxis-storage
    go build -o bin/praxis-network ./cmd/praxis-network
    go build -o bin/praxis-compute ./cmd/praxis-compute
    go build -o bin/praxis-identity ./cmd/praxis-identity
    go build -o bin/praxis-monitoring ./cmd/praxis-monitoring

# Build CLI binary only
build-cli:
    go build -o bin/praxis ./cmd/praxis

# Verify the compiled CLI against an already-running production process
# topology (Moto, Restate, Core, and all five production driver packs).
test-production-topology: build-cli
    go test -tags=acceptance ./tests/acceptance/ -v -count=1 -timeout=5m

# Generate JSON Schema artifacts for CloudEvent payload definitions.
generate-event-schemas:
    mkdir -p schemas/events/gen
    cue export schemas/events/command.cue --out jsonschema -e '#CommandApplyData' > schemas/events/gen/command-apply.json
    cue export schemas/events/command.cue --out jsonschema -e '#CommandDeleteData' > schemas/events/gen/command-delete.json
    cue export schemas/events/command.cue --out jsonschema -e '#CommandImportData' > schemas/events/gen/command-import.json
    cue export schemas/events/command.cue --out jsonschema -e '#CommandCancelData' > schemas/events/gen/command-cancel.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#DeploymentSubmittedData' > schemas/events/gen/deployment-submitted.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#DeploymentStartedData' > schemas/events/gen/deployment-started.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#DeploymentCompletedData' > schemas/events/gen/deployment-completed.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#DeploymentFailedData' > schemas/events/gen/deployment-failed.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#DeploymentCancelledData' > schemas/events/gen/deployment-cancelled.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#DeploymentDeleteStartedData' > schemas/events/gen/deployment-delete-started.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#DeploymentDeleteCompletedData' > schemas/events/gen/deployment-delete-completed.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#DeploymentDeleteFailedData' > schemas/events/gen/deployment-delete-failed.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#DeploymentApprovalRequestedData' > schemas/events/gen/deployment-approval-requested.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#DeploymentApprovalApprovedData' > schemas/events/gen/deployment-approval-approved.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#DeploymentApprovalRejectedData' > schemas/events/gen/deployment-approval-rejected.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#ResourceReplaceStartedData' > schemas/events/gen/resource-replace-started.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#ResourceAutoReplaceStartedData' > schemas/events/gen/resource-auto-replace-started.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#ResourceDispatchedData' > schemas/events/gen/resource-dispatched.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#ResourceReadyData' > schemas/events/gen/resource-ready.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#ResourceErrorData' > schemas/events/gen/resource-error.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#ResourceSkippedData' > schemas/events/gen/resource-skipped.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#ResourceDeleteStartedData' > schemas/events/gen/resource-delete-started.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#ResourceDeletedData' > schemas/events/gen/resource-deleted.json
    cue export schemas/events/lifecycle.cue --out jsonschema -e '#ResourceDeleteErrorData' > schemas/events/gen/resource-delete-error.json
    cue export schemas/events/drift.cue --out jsonschema -e '#DriftDetectedData' > schemas/events/gen/drift-detected.json
    cue export schemas/events/drift.cue --out jsonschema -e '#DriftCorrectedData' > schemas/events/gen/drift-corrected.json
    cue export schemas/events/drift.cue --out jsonschema -e '#DriftExternalDeleteData' > schemas/events/gen/drift-external-delete.json
    cue export schemas/events/policy.cue --out jsonschema -e '#PolicyPreventedDestroyData' > schemas/events/gen/policy-prevented-destroy.json
    cue export schemas/events/policy.cue --out jsonschema -e '#ForceDeleteOverrideData' > schemas/events/gen/policy-force-delete-override.json
    cue export schemas/events/system.cue --out jsonschema -e '#SinkRegisteredData' > schemas/events/gen/system-sink-registered.json
    cue export schemas/events/system.cue --out jsonschema -e '#SinkRemovedData' > schemas/events/gen/system-sink-removed.json
    cue export schemas/events/system.cue --out jsonschema -e '#SinkDeliveryFailedData' > schemas/events/gen/system-sink-delivery-failed.json
    cue export schemas/events/system.cue --out jsonschema -e '#RetentionEventData' > schemas/events/gen/system-retention-event.json

# Build Core binary only
build-core:
    go build -o bin/praxis-core ./cmd/praxis-core

# Build Docker images
docker-build:
    docker compose build

# ─── Test ───────────────────────────────────────────────────

# Run all unit tests (serialise packages to avoid Docker/port contention).
# Writes a coverage profile to coverage.out (atomic mode, safe under -race).
test:
    go test ./internal/... ./pkg/... -v -count=1 -race -p 1 -coverprofile=coverage.out -covermode=atomic

# Run Core unit tests (command service + DAG + orchestrator)
test-core:
    go test ./internal/core/command/... ./internal/core/dag/... ./internal/core/orchestrator/... -v -count=1 -race

# Run CLI unit tests
test-cli:
    go test ./internal/cli/... -v -count=1 -race

# Run S3 driver unit tests only
test-s3:
    go test ./internal/drivers/s3/... -v -count=1 -race

# Run EBS driver unit tests only
test-ebs:
    go test ./internal/drivers/ebs/... -v -count=1 -race

# Run EIP driver unit tests only
test-eip:
    go test ./internal/drivers/eip/... -v -count=1 -race

# Run ECR repository driver unit tests only
test-ecr-repo:
	go test ./internal/drivers/ecrrepo/... -v -count=1 -race

# Run ECR lifecycle policy driver unit tests only
test-ecr-policy:
	go test ./internal/drivers/ecrpolicy/... -v -count=1 -race

# Run all ECR driver unit tests
test-ecr: test-ecr-repo test-ecr-policy

# Run ACM certificate driver unit tests only
test-acmcert:
    go test ./internal/drivers/acmcert/... -v -count=1 -race

# Run IGW driver unit tests only
test-igw:
    go test ./internal/drivers/igw/... -v -count=1 -race

# Run NATGateway driver unit tests only
test-natgw:
	go test ./internal/drivers/natgw/... -v -count=1 -race

# Run NetworkACL driver unit tests only
test-nacl:
    go test ./internal/drivers/nacl/... -v -count=1 -race

# Run RouteTable driver unit tests only
test-routetable:
    go test ./internal/drivers/routetable/... -v -count=1 -race

# Run KeyPair driver unit tests only
test-keypair:
    go test ./internal/drivers/keypair/... -v -count=1 -race

# Run IAM Instance Profile driver unit tests only
test-iaminstanceprofile:
    go test ./internal/drivers/iaminstanceprofile/... -v -count=1 -race

# Run IAM Policy driver unit tests only
test-iampolicy:
    go test ./internal/drivers/iampolicy/... -v -count=1 -race

# Run IAM Role driver unit tests only
test-iamrole:
    go test ./internal/drivers/iamrole/... -v -count=1 -race

# Run IAM User driver unit tests only
test-iamuser:
    go test ./internal/drivers/iamuser/... -v -count=1 -race

# Run IAM Group driver unit tests only
test-iamgroup:
    go test ./internal/drivers/iamgroup/... -v -count=1 -race

# Run IAM driver unit tests only
test-iam:
    go test ./internal/drivers/iamrole/... ./internal/drivers/iampolicy/... ./internal/drivers/iamuser/... ./internal/drivers/iamgroup/... ./internal/drivers/iaminstanceprofile/... ./internal/core/provider/... -v -count=1 -race

# Run Route53 driver unit tests only
test-route53:
    go test ./internal/drivers/route53zone/... ./internal/drivers/route53record/... ./internal/drivers/route53healthcheck/... -v -count=1 -race

test-route53zone:
    go test ./internal/drivers/route53zone/... -v -count=1 -race

test-route53record:
    go test ./internal/drivers/route53record/... -v -count=1 -race

test-route53healthcheck:
    go test ./internal/drivers/route53healthcheck/... -v -count=1 -race

# Run EC2 driver unit tests only
test-ec2:
    go test ./internal/drivers/ec2/... -v -count=1 -race

# Run Lambda function driver unit tests only
test-lambda:
	go test ./internal/drivers/lambda/... ./internal/core/provider/... -v -count=1 -race

# Run RDS driver unit tests only
test-rds:
    go test ./internal/drivers/dbsubnetgroup/... ./internal/drivers/dbparametergroup/... ./internal/drivers/rdsinstance/... ./internal/drivers/auroracluster/... ./internal/core/provider/... -v -count=1 -race

# Run Lambda layer driver unit tests only
test-lambdalayer:
    go test ./internal/drivers/lambdalayer/... -v -count=1 -race

# Run Lambda permission driver unit tests only
test-lambdaperm:
    go test ./internal/drivers/lambdaperm/... -v -count=1 -race

# Run ELB driver pack unit tests (ALB, NLB, TG, Listener, Listener Rule)
test-elb:
    go test ./internal/drivers/alb/... ./internal/drivers/nlb/... ./internal/drivers/targetgroup/... ./internal/drivers/listener/... ./internal/drivers/listenerrule/... -v -count=1 -race

# Run SNS driver pack unit tests (Topic, Subscription)
test-sns:
    go test ./internal/drivers/snstopic/... ./internal/drivers/snssub/... ./internal/core/provider/... -v -count=1 -race

# Run SNS Topic driver unit tests only
test-snstopic:
    go test ./internal/drivers/snstopic/... -v -count=1 -race

# Run SNS Subscription driver unit tests only
test-snssub:
    go test ./internal/drivers/snssub/... -v -count=1 -race

# Run SQS queue driver unit tests only
test-sqs:
    go test ./internal/drivers/sqs/... -v -count=1 -race

# Run SQS queue policy driver unit tests only
test-sqspolicy:
    go test ./internal/drivers/sqspolicy/... -v -count=1 -race

# Run all SQS driver unit tests
test-sqs-all:
    go test ./internal/drivers/sqs/... ./internal/drivers/sqspolicy/... ./internal/core/provider/... -v -count=1 -race

# Run SQS queue integration tests
test-sqs-integration:
	go test ./tests/integration/ -run TestSQSQueue -v -count=1 -tags=integration -timeout=5m

# Run SQS queue policy integration tests
test-sqspolicy-integration:
	go test ./tests/integration/ -run TestSQSQueuePolicy -v -count=1 -tags=integration -timeout=5m

# Run SSM parameter driver unit tests
test-ssmparameter:
    go test ./internal/drivers/ssmparameter/... -v -count=1 -race

# Run SSM parameter integration tests
test-ssmparameter-integration:
	go test ./tests/integration/ -run TestSSMParameter -v -count=1 -tags=integration -timeout=5m

# Run CloudWatch monitoring driver tests.
test-monitoring:
    go test ./internal/drivers/loggroup/... ./internal/drivers/metricalarm/... ./internal/drivers/dashboard/... ./internal/core/provider/... -v -count=1 -race

test-loggroup:
    go test ./internal/drivers/loggroup/... -v -count=1 -race

test-metricalarm:
    go test ./internal/drivers/metricalarm/... -v -count=1 -race

test-dashboard:
    go test ./internal/drivers/dashboard/... -v -count=1 -race

# Run ALB driver unit tests only
test-alb:
    go test ./internal/drivers/alb/... -v -count=1 -race

# Run NLB driver unit tests only
test-nlb:
    go test ./internal/drivers/nlb/... -v -count=1 -race

# Run Target Group driver unit tests only
test-targetgroup:
    go test ./internal/drivers/targetgroup/... -v -count=1 -race

# Run Listener driver unit tests only
test-listener:
    go test ./internal/drivers/listener/... -v -count=1 -race

# Run Listener Rule driver unit tests only
test-listenerrule:
    go test ./internal/drivers/listenerrule/... -v -count=1 -race

# Run ACM certificate integration tests only
test-acm-integration:
	go test ./tests/integration/ -run TestACMCertificate -v -count=1 -tags=integration -timeout=5m

# Run Event Source Mapping driver unit tests only
test-esm:
    go test ./internal/drivers/esm/... -v -count=1 -race

# Run AMI driver unit tests only
test-ami:
    go test ./internal/drivers/ami/... -v -count=1 -race

# Run SG driver unit tests only
test-sg:
    go test ./internal/drivers/sg/... -v -count=1 -race

# Run Subnet driver unit tests only
test-subnet:
    go test ./internal/drivers/subnet/... -v -count=1 -race

# Run VPC driver unit tests only
test-vpc:
    go test ./internal/drivers/vpc/... -v -count=1 -race

# Run VPC peering driver unit tests only
test-vpcpeering:
    go test ./internal/drivers/vpcpeering/... -v -count=1 -race

# Run Subnet integration tests only
test-subnet-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestSubnet -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-subnet-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run IAM Policy integration tests only
test-iampolicy-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestIAMPolicy -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-iampolicy-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run IAM Role integration tests only
test-iamrole-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestIAMRole -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-iamrole-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run IAM User integration tests only
test-iamuser-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestIAMUser -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-iamuser-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run template engine + resolver unit tests
test-template:
    go test ./internal/core/template/... ./internal/core/resolver/... -v -count=1 -race

# Run integration tests (requires Docker — Testcontainers + Moto on :4566)
test-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    echo "Running integration tests (heartbeat every ${heartbeat}s, timeout {{integration_timeout}})..."
    go test ./tests/integration/... -v -count=1 -tags=integration -timeout={{integration_timeout}} &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run IAM Instance Profile integration tests only
test-iaminstanceprofile-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestIAMInstanceProfile -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-iaminstanceprofile-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run core E2E tests (requires Docker — full pipeline)
test-core-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestCore -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-core-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run full lifecycle tests (requires Docker — all 45 drivers, saas-platform.cue)
test-lifecycle:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestLifecycle -v -count=1 -tags=integration -timeout=20m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-lifecycle] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run SG integration tests (requires Docker — Testcontainers + Moto)
test-sg-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestSG -v -count=1 -tags=integration -timeout=5m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-sg-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run IGW integration tests (requires Docker — Testcontainers + Moto)
test-igw-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestIGW -v -count=1 -tags=integration -timeout=5m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-igw-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run NATGateway integration tests (requires Docker — Testcontainers + Moto)
test-natgw-integration:
	#!/bin/sh
	heartbeat={{test_heartbeat_seconds}}
	go test ./tests/integration/ -run TestNATGW -v -count=1 -tags=integration -timeout=10m &
	pid=$!
	while kill -0 "$pid" 2>/dev/null; do
		echo "[test-natgw-integration] still running at $(date +%H:%M:%S)"
		sleep "$heartbeat"
	done
	wait "$pid"

# Run NetworkACL integration tests (requires Docker — Testcontainers + Moto)
test-nacl-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestNACL -v -count=1 -tags=integration -timeout=5m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-nacl-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run EC2 integration tests (requires Docker — Testcontainers + Moto)
test-ec2-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestEC2 -v -count=1 -tags=integration -timeout=5m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-ec2-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run AMI integration tests (requires Docker — Testcontainers + Moto)
test-ami-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestAMI -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-ami-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run VPC integration tests (requires Docker — Testcontainers + Moto)
test-vpc-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestVPC -v -count=1 -tags=integration -timeout=5m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-vpc-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run EBS integration tests (requires Docker — Testcontainers + Moto)
test-ebs-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestEBS -v -count=1 -tags=integration -timeout=5m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-ebs-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run EIP integration tests (requires Docker — Testcontainers + Moto)
test-eip-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestEIP -v -count=1 -tags=integration -timeout=5m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-eip-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run KeyPair integration tests (requires Docker — Testcontainers + Moto)
test-keypair-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestKeyPair -v -count=1 -tags=integration -timeout=3m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-keypair-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run template integration tests (requires Docker — Moto SSM)
test-template-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestTemplate -v -count=1 -tags=integration -timeout=5m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-template-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run DBSubnetGroup integration tests (requires Docker — Testcontainers + Moto)
test-dbsubnetgroup-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestDBSubnetGroup -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-dbsubnetgroup-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run DBParameterGroup integration tests (requires Docker — Testcontainers + Moto)
test-dbparametergroup-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestDBParameterGroup -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-dbparametergroup-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run RDSInstance integration tests (requires Docker — Testcontainers + Moto)
test-rdsinstance-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestRDSInstance -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-rdsinstance-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run AuroraCluster integration tests (requires Docker — Testcontainers + Moto)
test-auroracluster-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestAuroraCluster -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-auroracluster-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run ALB integration tests (requires Docker — Testcontainers + Moto)
test-alb-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestALB -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-alb-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run NLB integration tests (requires Docker — Testcontainers + Moto)
test-nlb-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestNLB -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-nlb-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run Listener integration tests (requires Docker — Testcontainers + Moto)
test-listener-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestListener -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-listener-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run ListenerRule integration tests (requires Docker — Testcontainers + Moto)
test-listenerrule-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run TestListenerRule -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-listenerrule-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run ELB integration tests — all 5 ELB drivers (requires Docker — Testcontainers + Moto)
test-elb-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    go test ./tests/integration/ -run "TestALB|TestNLB|TestTargetGroup|TestListener|TestListenerRule" -v -count=1 -tags=integration -timeout=15m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-elb-integration] still running at $(date +%H:%M:%S)"
        sleep "$heartbeat"
    done
    wait "$pid"

# Run all tests
test-all: test test-integration

# ─── Lint & Format ──────────────────────────────────────────

# Lint all Go code
lint:
    golangci-lint run ./...

# Format code
fmt:
    gofmt -s -w .

# Check formatting (CI-friendly)
fmt-check:
    @test -z "$(gofmt -l .)" || (echo "unformatted files:" && gofmt -l . && exit 1)

# ─── CI ─────────────────────────────────────────────────────

# Regenerate event schemas and fail if they drift from the committed copies.
# The single source of truth for the drift gate, called by both `just ci` and
# the CI workflow so the two can never disagree.
check-schema-drift: generate-event-schemas
    @git diff --exit-code schemas/events/gen || (echo "event schemas are out of date — run 'just generate-event-schemas' and commit" && exit 1)

# Full local CI pipeline — mirrors .github/workflows/ci.yml so a green `just ci`
# means a green CI run: lint → fmt → unit tests → build → schema drift →
# integration tests.
ci: lint fmt-check test build check-schema-drift test-integration
    @echo "CI passed."

# ─── Moto Helpers ───────────────────────────────────────────

# Reset Moto state and re-run the seed script to restore pre-seeded data.
# Useful after delete/re-deploy cycles that leave stale idempotency tokens.
reset-moto:
    @echo "Resetting Moto state..."
    @curl -fsS -X POST http://localhost:4566/moto-api/reset >/dev/null
    @echo "✓ Moto state cleared"
    @echo "Re-seeding Moto..."
    @docker compose run --rm moto-init
    @echo "✓ Moto reset and re-seeded"

# List S3 buckets in Moto
ls-s3:
    aws --endpoint-url=http://localhost:4566 s3 ls

# List EBS volumes in Moto
ls-ebs:
    aws --endpoint-url=http://localhost:4566 ec2 describe-volumes --query 'Volumes[].{VolumeId:VolumeId,State:State,Size:Size,AZ:AvailabilityZone,Type:VolumeType}' --output table

# List Elastic IP allocations in Moto
ls-eip:
    aws --endpoint-url=http://localhost:4566 ec2 describe-addresses --query 'Addresses[].{AllocationId:AllocationId,PublicIp:PublicIp,AssociationId:AssociationId,InstanceId:InstanceId,Domain:Domain}' --output table

# List EC2 key pairs in Moto
ls-keypair:
    aws --endpoint-url=http://localhost:4566 ec2 describe-key-pairs --query 'KeyPairs[].{KeyName:KeyName,KeyPairId:KeyPairId,KeyType:KeyType,KeyFingerprint:KeyFingerprint}' --output table

# ─── Restate Helpers ────────────────────────────────────────

# List registered Restate deployments
rs-deployments:
    curl -s http://localhost:9070/deployments | jq .

# List registered Restate services
rs-services:
    curl -s http://localhost:9070/services | jq '.services[].name'

# Quick operator sanity check for the three most important local endpoints.
doctor:
    @echo "Checking Moto..."
    @curl -fsS http://localhost:4566/moto-api/ >/dev/null && echo "  ✓ Moto"
    @echo "Checking Restate admin..."
    @curl -fsS http://localhost:9070/health >/dev/null && echo "  ✓ Restate admin"
    @echo "Checking registered Restate services..."
    @curl -fsS http://localhost:9070/services | jq '.services[].name'

# ─── Alpha Release ──────────────────────────────────────────

# Praxis publishes one mutable alpha contract. The GitHub workflow advances the
# CLI archives, all six service images, the quick-start bundle, and the Helm
# chart together only when an owner dispatches it.

# Build every downloadable alpha artifact locally without publishing.
release-build:
    ./scripts/build-release.sh

# Verify checksums, the native CLI archive, image-only Compose wiring, and Helm.
release-verify: release-build
    ./scripts/verify-release.sh

# Run the local release gate. The full integration and production-topology
# acceptance suites remain explicit because they require Docker and can take
# more than 45 minutes.
release-preflight:
    just lint
    just fmt-check
    just test
    just build
    just release-verify
    @echo "Local alpha release gate passed."
    @echo "Run the GitHub Actions 'Release alpha' workflow only after owner approval."

# ─── Cleanup ────────────────────────────────────────────────

# Remove all build artifacts, volumes, and caches
clean: down
    rm -rf bin/ dist/
    go clean -cache -testcache
