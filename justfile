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

# Rebuild and restart the core + driver packs, then re-register them.
restart:
    just ensure-env
    docker compose up -d --build praxis-core praxis-storage praxis-network praxis-compute
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

# Follow logs for the network driver pack (SG, VPC).
logs-network:
    docker compose logs -f praxis-network

# Follow logs for the compute driver pack (EC2).
logs-compute:
    docker compose logs -f praxis-compute

# Follow logs for all driver packs together.
logs-drivers:
    docker compose logs -f praxis-storage praxis-network praxis-compute

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
    @curl -s -X POST http://localhost:9070/deployments \
        -H 'content-type: application/json' \
        -d '{"uri": "http://praxis-storage:9080"}' | jq .
    @echo "✓ Storage driver pack registered"
    @echo "Registering network driver pack with Restate..."
    @curl -s -X POST http://localhost:9070/deployments \
        -H 'content-type: application/json' \
        -d '{"uri": "http://praxis-network:9080"}' | jq .
    @echo "✓ Network driver pack registered"
    @echo "Registering compute driver pack with Restate..."
    @curl -s -X POST http://localhost:9070/deployments \
        -H 'content-type: application/json' \
        -d '{"uri": "http://praxis-compute:9080"}' | jq .
    @echo "✓ Compute driver pack registered"
    @echo "Registering Praxis Core (command service + orchestrator)..."
    @curl -s -X POST http://localhost:9070/deployments \
        -H 'content-type: application/json' \
        -d '{"uri": "http://praxis-core:9080"}' | jq .
    @echo "✓ Praxis core services registered"

# ─── Build ──────────────────────────────────────────────────

# Build everything: CLI, Core, and driver packs
build:
    go build -o bin/praxis ./cmd/praxis
    go build -o bin/praxis-core ./cmd/praxis-core
    go build -o bin/praxis-storage ./cmd/praxis-storage
    go build -o bin/praxis-network ./cmd/praxis-network
    go build -o bin/praxis-compute ./cmd/praxis-compute

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

# Run EC2 driver unit tests only
test-ec2:
    go test ./internal/drivers/ec2/... -v -count=1 -race

# Run AMI driver unit tests only
test-ami:
    go test ./internal/drivers/ami/... -v -count=1 -race

# Run SG driver unit tests only
test-sg:
    go test ./internal/drivers/sg/... -v -count=1 -race

# Run VPC driver unit tests only
test-vpc:
    go test ./internal/drivers/vpc/... -v -count=1 -race

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

# Run EC2 integration tests (requires Docker — Testcontainers + LocalStack)
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

# Run AMI integration tests (requires Docker — Testcontainers + LocalStack)
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

# Run VPC integration tests (requires Docker — Testcontainers + LocalStack)
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

# Semver convention:
#   Major — big architecture changes (shared, post-1.0.0)
#   Minor — driver-level releases / new features
#   Patch — hotfixes and patches
#
# Mass release workflow (all services + CLI):
#   1. just release-preflight v0.3.0   — validate, test, build everything
#   2. just release v0.3.0             — tag + push → triggers GitHub Actions
#
# Per-service release workflow (single service):
#   1. just release-service-preflight praxis-network v0.2.1
#   2. just release-service praxis-network v0.2.1
#
# Per-service tags use the format SERVICE/vX.Y.Z (e.g. praxis-network/v0.2.1).
# Mass release tags use vX.Y.Z.
#
# Valid services: praxis, praxis-core, praxis-storage, praxis-network, praxis-compute
#
# GitHub Actions builds the artifacts and creates the GitHub Release.
# You write the release notes in the GitHub UI (or pass --notes to gh).

# Validate version format (vMAJOR.MINOR.PATCH)
_validate-version VERSION:
    #!/bin/sh
    echo "{{VERSION}}" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?$' \
        || (echo "Invalid version: {{VERSION}}. Must match vMAJOR.MINOR.PATCH[-prerelease]" && exit 1)
    echo "✓ Version {{VERSION}} is valid semver"

# Validate that SERVICE is a known Praxis component
_validate-service SERVICE:
    #!/bin/sh
    case "{{SERVICE}}" in
        praxis|praxis-core|praxis-storage|praxis-network|praxis-compute) ;;
        *) echo "Unknown service: {{SERVICE}}"
           echo "Valid services: praxis, praxis-core, praxis-storage, praxis-network, praxis-compute"
           exit 1 ;;
    esac
    echo "✓ Service {{SERVICE}} is valid"

# Run pre-release checks: lint, unit tests, build all binaries
release-preflight VERSION: (_validate-version VERSION)
    @echo "═══ Pre-release checks for {{VERSION}} ═══"
    @echo ""
    @echo "→ Checking working tree is clean..."
    @git diff --quiet --exit-code || (echo "ERROR: working tree has uncommitted changes" && exit 1)
    @git diff --cached --quiet --exit-code || (echo "ERROR: index has staged changes" && exit 1)
    @echo "✓ Working tree clean"
    @echo ""
    @echo "→ Running lint..."
    just lint
    @echo ""
    @echo "→ Running unit tests..."
    just test
    @echo ""
    @echo "→ Building all binaries..."
    just build
    @echo ""
    @echo "✓ Pre-release checks passed for {{VERSION}}"
    @echo "  Run 'just release {{VERSION}}' to tag and push."

# Build release artifacts locally (for inspection before tagging)
release-build VERSION: (_validate-version VERSION)
    #!/bin/sh
    set -eu
    VERSION="{{VERSION}}"
    DATE=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
    DIST="dist/${VERSION}"
    LDFLAGS="-X github.com/praxiscloud/praxis/internal/cli.version=${VERSION} \
             -X github.com/praxiscloud/praxis/internal/cli.buildDate=${DATE}"

    mkdir -p "${DIST}"

    echo "Building CLI binaries..."
    for GOOS_GOARCH in darwin/arm64 darwin/amd64 linux/amd64; do
        GOOS="${GOOS_GOARCH%/*}"
        GOARCH="${GOOS_GOARCH#*/}"
        OUT="${DIST}/praxis_${GOOS}_${GOARCH}"
        mkdir -p "${OUT}"
        echo "  ${GOOS}/${GOARCH}"
        GOOS=${GOOS} GOARCH=${GOARCH} go build -ldflags "${LDFLAGS}" -o "${OUT}/praxis" ./cmd/praxis
        tar -czf "${DIST}/praxis_${GOOS}_${GOARCH}.tar.gz" -C "${OUT}" praxis
    done

    echo "Building service binaries (linux/amd64)..."
    mkdir -p "${DIST}/linux_amd64"
    for SVC in praxis-core praxis-storage praxis-network praxis-compute; do
        echo "  ${SVC}"
        GOOS=linux GOARCH=amd64 go build -ldflags "${LDFLAGS}" -o "${DIST}/linux_amd64/${SVC}" "./cmd/${SVC}"
    done

    echo "Generating checksums..."
    cd "${DIST}" && shasum -a 256 *.tar.gz > checksums.txt

    echo ""
    echo "Release ${VERSION} built successfully:"
    echo "  CLI tarballs:  ${DIST}/praxis_*.tar.gz"
    echo "  Services:      ${DIST}/linux_amd64/"
    echo "  Checksums:     ${DIST}/checksums.txt"

# Tag the release and push to GitHub — triggers the release workflow
release VERSION: (_validate-version VERSION)
    #!/bin/sh
    set -e

    # Verify tag doesn't already exist
    if git rev-parse "{{VERSION}}" >/dev/null 2>&1; then
        echo "ERROR: tag {{VERSION}} already exists"
        exit 1
    fi

    # Verify we're on main
    branch=$(git rev-parse --abbrev-ref HEAD)
    if [ "$branch" != "main" ]; then
        echo "ERROR: releases must be cut from main (currently on $branch)"
        exit 1
    fi

    # Verify clean working tree
    if ! git diff --quiet --exit-code || ! git diff --cached --quiet --exit-code; then
        echo "ERROR: working tree has uncommitted changes"
        exit 1
    fi

    echo "Creating release tag {{VERSION}}..."
    git tag -a "{{VERSION}}" -m "Release {{VERSION}}"
    echo "✓ Tag created"

    echo "Pushing tag to origin..."
    git push origin "{{VERSION}}"
    echo "✓ Tag pushed — GitHub Actions will create the release"
    echo ""
    echo "Next steps:"
    echo "  1. Go to https://github.com/shirvan/praxis/actions to monitor the build"
    echo "  2. Once complete, edit the release at https://github.com/shirvan/praxis/releases/tag/{{VERSION}}"
    echo "     to add release notes describing what changed."

# ─── Per-Service Release ────────────────────────────────────

# Run pre-release checks for a single service: lint, service-specific tests, build
# Usage: just release-service-preflight praxis-network v0.2.1
release-service-preflight SERVICE VERSION: (_validate-service SERVICE) (_validate-version VERSION)
    #!/bin/sh
    set -e
    echo "═══ Pre-release checks for {{SERVICE}} {{VERSION}} ═══"
    echo ""
    echo "→ Checking working tree is clean..."
    git diff --quiet --exit-code || (echo "ERROR: working tree has uncommitted changes" && exit 1)
    git diff --cached --quiet --exit-code || (echo "ERROR: index has staged changes" && exit 1)
    echo "✓ Working tree clean"
    echo ""
    echo "→ Running lint..."
    just lint
    echo ""
    echo "→ Running {{SERVICE}} tests..."
    case "{{SERVICE}}" in
        praxis)          go test ./internal/cli/... -v -count=1 -race ;;
        praxis-core)     go test ./internal/core/command/... ./internal/core/dag/... ./internal/core/orchestrator/... -v -count=1 -race ;;
        praxis-storage)  go test ./internal/drivers/s3/... -v -count=1 -race ;;
        praxis-network)  go test ./internal/drivers/sg/... ./internal/drivers/vpc/... -v -count=1 -race ;;
        praxis-compute)  go test ./internal/drivers/ec2/... -v -count=1 -race ;;
    esac
    echo ""
    echo "→ Building {{SERVICE}}..."
    just release-service-build {{SERVICE}} {{VERSION}}
    echo ""
    echo "✓ Pre-release checks passed for {{SERVICE}} {{VERSION}}"
    echo "  Run 'just release-service {{SERVICE}} {{VERSION}}' to tag and push."

# Build a single service's release artifacts locally
# Usage: just release-service-build praxis-network v0.2.1
release-service-build SERVICE VERSION: (_validate-service SERVICE) (_validate-version VERSION)
    #!/bin/sh
    set -eu
    VERSION="{{VERSION}}"
    SERVICE="{{SERVICE}}"
    DATE=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
    DIST="dist/${SERVICE}/${VERSION}"
    LDFLAGS="-X github.com/praxiscloud/praxis/internal/cli.version=${VERSION} \
             -X github.com/praxiscloud/praxis/internal/cli.buildDate=${DATE}"

    mkdir -p "${DIST}"

    if [ "${SERVICE}" = "praxis" ]; then
        echo "Building CLI binaries..."
        for GOOS_GOARCH in darwin/arm64 darwin/amd64 linux/amd64; do
            GOOS="${GOOS_GOARCH%/*}"
            GOARCH="${GOOS_GOARCH#*/}"
            OUT="${DIST}/praxis_${GOOS}_${GOARCH}"
            mkdir -p "${OUT}"
            echo "  ${GOOS}/${GOARCH}"
            GOOS=${GOOS} GOARCH=${GOARCH} go build -ldflags "${LDFLAGS}" -o "${OUT}/praxis" ./cmd/praxis
            tar -czf "${DIST}/praxis_${GOOS}_${GOARCH}.tar.gz" -C "${OUT}" praxis
        done
        echo "Generating checksums..."
        cd "${DIST}" && shasum -a 256 *.tar.gz > checksums.txt
    else
        echo "Building ${SERVICE} (linux/amd64)..."
        GOOS=linux GOARCH=amd64 go build -ldflags "${LDFLAGS}" -o "${DIST}/${SERVICE}" "./cmd/${SERVICE}"
    fi

    echo ""
    echo "Release ${SERVICE} ${VERSION} built successfully:"
    echo "  Artifacts: ${DIST}/"

# Tag a single service and push to GitHub
# Usage: just release-service praxis-network v0.2.1
# Creates tag: praxis-network/v0.2.1
release-service SERVICE VERSION: (_validate-service SERVICE) (_validate-version VERSION)
    #!/bin/sh
    set -e
    TAG="{{SERVICE}}/{{VERSION}}"

    # Verify tag doesn't already exist
    if git rev-parse "${TAG}" >/dev/null 2>&1; then
        echo "ERROR: tag ${TAG} already exists"
        exit 1
    fi

    # Verify we're on main
    branch=$(git rev-parse --abbrev-ref HEAD)
    if [ "$branch" != "main" ]; then
        echo "ERROR: releases must be cut from main (currently on $branch)"
        exit 1
    fi

    # Verify clean working tree
    if ! git diff --quiet --exit-code || ! git diff --cached --quiet --exit-code; then
        echo "ERROR: working tree has uncommitted changes"
        exit 1
    fi

    echo "Creating release tag ${TAG}..."
    git tag -a "${TAG}" -m "Release {{SERVICE}} {{VERSION}}"
    echo "✓ Tag created"

    echo "Pushing tag to origin..."
    git push origin "${TAG}"
    echo "✓ Tag pushed — GitHub Actions will create the release"
    echo ""
    echo "Next steps:"
    echo "  1. Go to https://github.com/shirvan/praxis/actions to monitor the build"
    echo "  2. Once complete, edit the release at https://github.com/shirvan/praxis/releases/tag/${TAG}"
    echo "     to add release notes describing what changed."

# ─── Cleanup ────────────────────────────────────────────────

# Remove all build artifacts, volumes, and caches
clean: down
    rm -rf bin/ dist/
    go clean -cache -testcache
