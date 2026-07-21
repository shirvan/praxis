---
title: Plan and deploy
description: Preview and apply dependency-aware infrastructure changes.
sidebar:
  order: 1
---

Planning evaluates the complete template pipeline without provisioning resources. Deployment submits the evaluated graph to the durable orchestrator.

## Preview changes

```bash
praxis plan stack.cue \
  --account prod \
  -f prod.vars.json
```

The plan compares desired resources with stored Praxis state and live provider observations. It reports creates, updates, replacements, deletes, and unchanged resources with field-level differences when the driver exposes them.

Save a plan for review:

```bash
praxis plan stack.cue \
  --account prod \
  -f prod.vars.json \
  --out payments.plan.json
```

## Apply a reviewed plan

```bash
praxis deploy \
  --plan payments.plan.json \
  --key payments-prod \
  --wait
```

Praxis checks the saved plan’s content hash and age before applying it. Signed plans can add an integrity boundary for a review pipeline.

## Useful controls

```bash
# Plan only one resource and its dependencies.
praxis plan stack.cue --account prod --target application

# Force an explicit replacement.
praxis deploy stack.cue --account prod --replace application

# Allow replacement when immutable fields changed.
praxis deploy stack.cue --account prod --allow-replace

# Return immediately after submission.
praxis deploy stack.cue --account prod --key payments-prod
```

Independent graph nodes run concurrently. `--parallelism` can impose a deployment-wide limit when provider quotas or change-management policy require it.

## Failure behavior

A failed resource stops its dependents and leaves independent completed resources in their known state. Praxis does not automatically roll back successful resources after every deployment failure. Operators can inspect the causal chain and choose deletion, a corrected apply, or a point-in-time rollback.

:::tip[Full reference]
For deployment scheduling, durable state, failure propagation, rollback, and recovery, see [Orchestration in the GitHub documentation](https://github.com/shirvan/praxis/blob/main/docs/ORCHESTRATOR.md). For every command option, see the [CLI reference](https://github.com/shirvan/praxis/blob/main/docs/CLI.md).
:::
