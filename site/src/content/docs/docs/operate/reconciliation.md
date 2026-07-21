---
title: Reconciliation and drift
description: Understand how Praxis compares, reports, and corrects infrastructure drift.
sidebar:
  order: 3
---

Reconciliation compares a resource’s stored desired specification with its current AWS state. Praxis can run it from a durable timer or on demand.

```bash
praxis reconcile VPC/us-west-2~payments
```

## Managed correction enabled

When `managedDriftCorrection` is enabled:

1. The driver describes the live AWS resource.
2. It computes differences from the desired specification.
3. It records drift conditions and an event.
4. It applies supported corrective changes.
5. It observes the resource again and records convergence.

If an externally deleted resource is still desired, the reconciler provisions it again. The declared resource still exists in Praxis, so absence in AWS is drift from user intent.

## Managed correction disabled

When correction is disabled, reconciliation performs no provider writes. Praxis reports the drift and keeps the resource operationally healthy because visibility-only behavior is the desired policy.

This is not a silent success. The resource retains a drift condition with the observed difference so users and agents can distinguish “matching AWS” from “healthy but externally changed.”

## Error recovery

Transient provider failures are returned to Restate from inside the journaled handler callback. Restate applies durable retry semantics. Terminal validation, authorization, conflict, and not-found outcomes are classified explicitly so they do not retry forever.

A user can trigger provisioning after correcting a terminal resource error. Retry policy and timeout remain configurable at deployment level.

## Conditions

Conditions are persisted with generic resource state. They provide context beyond a single status value, including readiness, drift, and the latest reconciliation outcome.

:::tip[Full reference]
For the reconciliation algorithm, drift policy, external deletion, conditions, retry behavior, and lifecycle hooks, see [Generic drivers in the GitHub documentation](https://github.com/shirvan/praxis/blob/main/docs/GENERIC_DRIVERS.md).
:::
