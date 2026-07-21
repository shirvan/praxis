---
title: HTTP API
description: Call Praxis operations directly through Restate ingress.
sidebar:
  order: 3
---

The CLI and direct clients use the same Restate ingress surface. Requests are JSON and target a registered Praxis service handler.

```bash
curl -X POST http://localhost:8080/PraxisCommandService/Apply \
  -H 'content-type: application/json' \
  -d '{
    "template": "dev-instance",
    "account": "prod",
    "variables": {"environment": "staging"}
  }'
```

## Main surfaces

- `PraxisCommandService` accepts plan, apply, import, delete, approval, rollback, and discovery commands.
- Deployment Virtual Objects expose current state, detail, events, and generations.
- Resource Virtual Objects expose lifecycle handlers and read-only status, outputs, and inputs.
- Workspace, template, auth, sink, and retention services expose their specific configuration contracts.

## Addressing

Restate ingress uses:

```text
/<Service>/<Handler>
/<VirtualObject>/<Key>/<Handler>
/<Workflow>/<Key>/<Handler>
```

All state-changing calls should use the command service unless you are integrating with an explicitly documented lower-level service contract.

:::tip[Full reference]
For every service, handler, and payload shape, see the [complete API reference on GitHub](https://github.com/shirvan/praxis/blob/main/docs/API.md) and the [OpenAPI document](https://github.com/shirvan/praxis/blob/main/api/openapi.yaml).
:::
