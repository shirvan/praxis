# justfile — Praxis task runner
# Install: https://github.com/casey/just
# Usage: just <recipe>

set dotenv-load := true

# Default: show available recipes
default:
    @just --list

wait_timeout_seconds := "120"
test_heartbeat_seconds := "30"

# ─── Development ────────────────────────────────────────────

# Start the full local stack and register every Praxis endpoint with
# Restate after the infra becomes reachable.
up:
    just ensure-env
    docker compose up -d --build
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
    echo "Waiting for LocalStack health endpoint (timeout: ${timeout}s)..."
    until curl -fsS http://localhost:4566/_localstack/health >/dev/null; do
        now=$(date +%s)
        elapsed=$((now-start))
        if [ "$elapsed" -ge "$timeout" ]; then
            echo "Timed out waiting for LocalStack after ${elapsed}s"
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

# Rebuild and restart the core + driver services, then re-register them.
restart:
    just ensure-env
    docker compose up -d --build praxis-core praxis-s3 praxis-sg
    just wait-stack
    just register

# Show current container status for the full stack.
status:
    docker compose ps

# Follow logs for Praxis Core.
logs:
    docker compose logs -f praxis-core

# Follow logs for the S3 driver only.
logs-s3:
    docker compose logs -f praxis-s3

# Follow logs for the SG driver only.
logs-sg:
    docker compose logs -f praxis-sg

# Follow logs for both drivers together.
logs-drivers:
    docker compose logs -f praxis-s3 praxis-sg

# Follow logs for all services
logs-all:
    docker compose logs -f

# Register all services with Restate (drivers + core).
#
# The admin API registration step is intentionally kept explicit in the justfile
# instead of being hidden in a shell script. That keeps local troubleshooting
# easy and gives AI agents a single canonical place to learn the deployment URIs.
register:
    @echo "Registering S3 driver with Restate..."
    @curl -s -X POST http://localhost:9070/deployments \
        -H 'content-type: application/json' \
        -d '{"uri": "http://praxis-s3:9080"}' | jq .
    @echo "✓ S3 driver registered"
    @echo "Registering SG driver with Restate..."
    @curl -s -X POST http://localhost:9070/deployments \
        -H 'content-type: application/json' \
        -d '{"uri": "http://praxis-sg:9080"}' | jq .
    @echo "✓ SG driver registered"
    @echo "Registering Praxis Core (command service + orchestrator)..."
    @curl -s -X POST http://localhost:9070/deployments \
        -H 'content-type: application/json' \
        -d '{"uri": "http://praxis-core:9080"}' | jq .
    @echo "✓ Praxis core services registered"

# ─── Build ──────────────────────────────────────────────────

# Build everything: CLI, Core, and drivers
build:
    go build -o bin/praxis ./cmd/praxis
    go build -o bin/praxis-core ./cmd/praxis-core
    go build -o bin/praxis-s3 ./cmd/praxis-s3
    go build -o bin/praxis-sg ./cmd/praxis-sg

# Build CLI binary only
build-cli:
    go build -o bin/praxis ./cmd/praxis

# Build Core binary only
build-core:
    go build -o bin/praxis-core ./cmd/praxis-core

# Build Docker images
docker-build:
    docker compose build

# ─── Test ───────────────────────────────────────────────────

# Run all unit tests (no Docker needed)
test:
    go test ./internal/... ./pkg/... -v -count=1 -race

# Run Core unit tests (command service + DAG + orchestrator)
test-core:
    go test ./internal/core/command/... ./internal/core/dag/... ./internal/core/orchestrator/... -v -count=1 -race

# Run CLI unit tests
test-cli:
    go test ./internal/cli/... -v -count=1 -race

# Run S3 driver unit tests only
test-s3:
    go test ./internal/drivers/s3/... -v -count=1 -race

# Run SG driver unit tests only
test-sg:
    go test ./internal/drivers/sg/... -v -count=1 -race

# Run template engine + resolver unit tests
test-template:
    go test ./internal/core/template/... ./internal/core/resolver/... -v -count=1 -race

# Run integration tests (requires Docker — Testcontainers + LocalStack)
test-integration:
    #!/bin/sh
    heartbeat={{test_heartbeat_seconds}}
    echo "Running integration tests (heartbeat every ${heartbeat}s)..."
    go test ./tests/integration/... -v -count=1 -tags=integration -timeout=10m &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        echo "[test-integration] still running at $(date +%H:%M:%S)"
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

# Run SG integration tests (requires Docker — Testcontainers + LocalStack)
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

# Run template integration tests (requires Docker — LocalStack SSM)
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

# Full local CI pipeline: lint → unit tests → integration tests
ci: lint test test-integration
    @echo "CI passed."

# ─── LocalStack Helpers ─────────────────────────────────────

# List S3 buckets in LocalStack
ls-s3:
    aws --endpoint-url=http://localhost:4566 s3 ls

# ─── Restate Helpers ────────────────────────────────────────

# List registered Restate deployments
rs-deployments:
    curl -s http://localhost:9070/deployments | jq .

# List registered Restate services
rs-services:
    curl -s http://localhost:9070/services | jq '.services[].name'

# Quick operator sanity check for the three most important local endpoints.
doctor:
    @echo "Checking LocalStack..."
    @curl -fsS http://localhost:4566/_localstack/health >/dev/null && echo "  ✓ LocalStack"
    @echo "Checking Restate admin..."
    @curl -fsS http://localhost:9070/health >/dev/null && echo "  ✓ Restate admin"
    @echo "Checking registered Restate services..."
    @curl -fsS http://localhost:9070/services | jq '.services[].name'

# ─── Release ────────────────────────────────────────────────

# Build release artifacts for the given version
release VERSION:
    hack/release.sh {{VERSION}}

# ─── Cleanup ────────────────────────────────────────────────

# Remove all build artifacts, volumes, and caches
clean: down
    rm -rf bin/ dist/
    go clean -cache -testcache
