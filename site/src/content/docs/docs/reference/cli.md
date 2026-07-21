---
title: CLI reference
description: The Praxis command surface for people, scripts, and agents.
sidebar:
  order: 2
---

The `praxis` CLI uses a verb-first command grammar. Every command supports `-o json` for machine-readable output.

## Core commands

| Command | Purpose |
| --- | --- |
| `praxis plan` | Evaluate a template and preview resource changes |
| `praxis deploy` | Submit or apply a deployment |
| `praxis get` | Read a deployment, resource, schema, or configuration |
| `praxis list` | List deployments, resources, events, schemas, and metadata |
| `praxis delete` | Delete a managed deployment or resource |
| `praxis import` | Adopt an existing provider resource |
| `praxis reconcile` | Trigger immediate drift comparison and correction policy |
| `praxis observe` | Follow a deployment event stream or resource state |
| `praxis rollback` | Apply a previous complete deployment generation |
| `praxis approve` | Resume a deployment waiting at an approval gate |
| `praxis reject` | Cancel a deployment waiting at an approval gate |
| `praxis create` | Create a workspace, template, or notification sink |
| `praxis set` | Select a workspace or change workspace configuration |

## Global options

```text
--endpoint   Restate ingress URL
-o           table or json output
--plain      disable terminal styling
--region     default AWS region
```

Provider operations also accept `--account` or use `PRAXIS_ACCOUNT`.

## Examples

```bash
praxis plan stack.cue --account prod -f prod.vars.json
praxis deploy stack.cue --account prod --key payments --wait
praxis get Deployment/payments --all
praxis list S3Bucket -w production
praxis observe Deployment/payments -o json
praxis reconcile S3Bucket/payments-prod-archive
```

## Exit behavior

Human-readable errors are written for interactive use. JSON output returns a stable error envelope for scripts and agents. Transient provider failures may remain inside durable Restate retry handling instead of returning immediately to the CLI.

:::tip[Full reference]
For every command, flag, output mode, environment variable, and exit behavior, see the [complete CLI reference on GitHub](https://github.com/shirvan/praxis/blob/main/docs/CLI.md).
:::
