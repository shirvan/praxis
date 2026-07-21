---
title: Security model
description: Credentials, ingress trust, secrets, and lifecycle protections in Praxis.
sidebar:
  order: 4
---

Praxis treats its ingress boundary as trusted operator access. Protect Restate ingress and administration endpoints with the network and identity controls appropriate for your environment.

## AWS credentials

Credential configurations are named accounts. Drivers request credentials at provider-call time through the Auth Service. Prefer role assumption for real AWS environments so long-lived access keys are not stored in templates or deployment specifications.

## Template secrets

Templates can reference values through the supported SSM secret protocol. Secret material should not be committed to CUE files, variables files, or example plans.

## Plan review

Saved plans can be reviewed separately from application. Praxis checks content integrity and plan age before use. Protected workspaces can suspend deployments at a durable approval gate.

## Destructive operations

Use `lifecycle.preventDestroy` for resources whose deletion needs explicit template change. Replacement remains a destructive operation and requires explicit command policy when an immutable field changes.

## Events and outputs

Treat event sinks, stored outputs, CLI JSON, and Restate state as operational data. Avoid exposing provider secrets as resource outputs. Restrict sink destinations and protect backups of the Restate state and log.

:::tip[Full reference]
For credential sources and account boundaries, see [Accounts and authentication](https://github.com/shirvan/praxis/blob/main/docs/AUTH.md). For production hardening and operational controls, see the [operator guide](https://github.com/shirvan/praxis/blob/main/docs/OPERATORS.md).
:::
