# Praxis — Agent Guide

This repository is structured for AI agent consumption. Start here.

## Entry Points

| What you need | Where to look |
|--------------|---------------|
| **Understand the project** | `docs/INDEX.md` → master directory of all docs |
| **Do a specific task** | `skills/MANIFEST.md` → registry of step-by-step skills |
| **Find code** | `docs/CODEBASE.md` → directory map with key files |
| **Look up a term** | `docs/GLOSSARY.md` → A-Z glossary |
| **Call the HTTP API** | `docs/API.md` → every operation as a Restate ingress call |

## Repository Layout

```text
cmd/            7 binary entry points (CLI, core, 5 driver packs)
internal/       Core logic + 51 resource drivers
pkg/types/      Shared types used across packages
schemas/        CUE schemas for AWS resources, events, notifications
examples/       Example CUE templates
tests/          Integration + production-topology acceptance tests
deploy/         Published artifact inputs, including the no-clone quick start
scripts/        Local alpha artifact build and verification
docs/           Documentation — docs/INDEX.md is the directory
skills/         Agent task skills (step-by-step procedures)
```

## How to Use This Repo as an Agent

1. **Start with `docs/INDEX.md`** to find the right doc for the area you're touching.
2. **Follow a skill** from `skills/MANIFEST.md` if performing a common task
   (implement a driver, add an adapter, write tests, author/migrate templates).
3. **Use `docs/CODEBASE.md`** to locate code — it maps tasks to entry-point files.

## Working Agreements

- Every CLI command supports `-o json` for machine-readable output. The HTTP API
  (Restate ingress) is the same surface the CLI uses — see `docs/API.md`.
- Error classification MUST happen inside `restate.Run()` callbacks: terminal
  errors (validation/conflict/not-found) are wrapped with `restate.TerminalError()`,
  transient ones returned bare. See `docs/ERRORS.md`.
- Unit tests: `go test ./internal/... ./pkg/...` (some orchestrator/driver tests
  start a Restate testcontainer and need Docker running).
- Integration tests: `go test -tags integration ./tests/integration/` (needs Docker).
- Production-topology acceptance: `just test-production-topology` after starting
  and registering the full Compose stack.
- Lint/format: `golangci-lint run` and `gofmt` (see `.golangci.yml`).
- Alpha contracts support exactly one version: `alpha`. Keep explicit version
  fields, but do not add migrations, backward-compatible reads, aliases, or
  parallel implementations without explicit owner approval. Breaking old alpha
  state, plans, and templates is acceptable.
- The only supported release and Praxis image tag is mutable `alpha`. Do not add
  numbered, `latest`, or per-service release paths without owner approval.

## Key Files

- `justfile` — All build, test, and run recipes
- `go.mod` — Dependencies and Go version
- `docker-compose.yaml` — Full local stack definition
- `Dockerfile` — Multi-stage build for all binaries
