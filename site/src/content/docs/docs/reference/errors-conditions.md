---
title: Errors and conditions
description: Understand retryable failures, terminal errors, status, and persisted conditions.
sidebar:
  order: 4
---

Praxis separates provider failures by whether retrying can make progress.

## Retryable failures

Throttling, transport interruptions, and provider availability failures are returned to Restate from inside the journaled callback. Restate retries them durably with its configured policy.

## Terminal failures

Validation, authorization, conflict, and confirmed not-found outcomes are wrapped as terminal errors inside the same callback. They stop retrying and preserve a user-actionable failure.

| Status | Meaning |
| --- | --- |
| `400` | Invalid request or provider validation |
| `401` | Missing or expired credentials |
| `403` | Access denied |
| `404` | Requested resource not found |
| `409` | Conflict, ambiguity, immutable change, or lifecycle policy |
| `500` | Terminal internal failure |

## Conditions

A status such as `Ready` describes the current lifecycle phase. Conditions add durable context about why that status is true.

Typical condition concepts include:

- Readiness and latest successful observation
- Detected drift and field differences
- Whether correction was attempted
- External deletion
- Latest provider or lifecycle failure

A visibility-only resource may be `Ready` while a `Drifted` condition remains present. That is intentional: the resource is meeting the configured no-write policy while still exposing the difference from its stored declaration.

:::tip[Full reference]
For error classes, stable codes, retry rules, and implementation requirements, see the [error model on GitHub](https://github.com/shirvan/praxis/blob/main/docs/ERRORS.md).
:::
