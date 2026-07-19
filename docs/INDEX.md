# Praxis Documentation Index

> One table, all docs. Start here whether you're a human or an agent.

| Document | Audience | One-line summary |
|----------|----------|------------------|
| [PRAXIS_ARCHITECTURE.md](PRAXIS_ARCHITECTURE.md) | Everyone | How Praxis works — Restate-powered core, Virtual Object drivers, design tradeoffs |
| [CODEBASE.md](CODEBASE.md) | Contributors | Directory map, binaries, key files, where to start for common tasks |
| [GLOSSARY.md](GLOSSARY.md) | Everyone | A–Z definitions of Praxis terms |
| [CLI.md](CLI.md) | Users | All commands, output formats, exit codes, timeouts |
| [API.md](API.md) | Integrators / Agents | The HTTP API — every operation as a Restate ingress call, with OpenAPI spec |
| [TEMPLATES.md](TEMPLATES.md) | Platform Engineers | CUE template system: variables, expressions, data sources, policies, lifecycle rules |
| [ORCHESTRATOR.md](ORCHESTRATOR.md) | Contributors | Deployment workflows, DAG scheduling, state lifecycle, delete/rollback flows |
| [DRIVERS.md](DRIVERS.md) | Contributors | Driver model, 8-handler contract, state, drift detection, building new drivers |
| [GENERIC_DRIVERS.md](GENERIC_DRIVERS.md) | Contributors | Generic lifecycle kernel, one production shape, and alpha version policy |
| [AUTH.md](AUTH.md) | Everyone | Credential management, workspaces, account selection |
| [EVENTS.md](EVENTS.md) | Contributors | CloudEvents pipeline, event types, webhook sinks, retention |
| [ERRORS.md](ERRORS.md) | Contributors | Error classification, status codes, stable error codes |
| [OPERATORS.md](OPERATORS.md) | Operators | Deployment, configuration, registration, monitoring, troubleshooting |
| [DEVELOPERS.md](DEVELOPERS.md) | Contributors | Building, testing, project structure, contributing |
| [EXTENDING.md](EXTENDING.md) | Contributors | Custom drivers in any language without forking |
| [DRIVER_ROADMAP.md](DRIVER_ROADMAP.md) | Everyone | Planned driver coverage |
| [FUTURE.md](FUTURE.md) | Everyone | Where Praxis is going |

## Task-oriented guides (skills)

Step-by-step procedures live in [`skills/`](../skills/MANIFEST.md) — implement a driver,
add an adapter, write tests, author or migrate templates, debug a deployment, extend the CLI.

## For AI agents

[`AGENTS.md`](../AGENTS.md) at the repo root is the entry point. The short version:
read this index, open the doc for the area you're touching, follow a skill for
common tasks, and use [CODEBASE.md](CODEBASE.md) to find code.
