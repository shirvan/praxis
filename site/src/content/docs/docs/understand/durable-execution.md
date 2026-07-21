---
title: Durable execution
description: Why Praxis uses Restate for resource state, retries, timers, and crash recovery.
sidebar:
  order: 3
---

Infrastructure operations cross process, network, and provider boundaries. A conventional request handler can lose its progress when any of those boundaries fail. Praxis uses Restate so execution history and resource state outlive an individual service process.

```d2
direction: down

request: "Provision request"
journal: "Restate journal" {
  shape: cylinder
}
driver: "Resource driver"
aws: "AWS API" {
  shape: cloud
}
restart: "process restarts" {
  style.stroke-dash: 4
}

request -> driver
driver -> journal: "record step"
journal -> aws: "run provider call once"
aws -> journal: "record result"
restart -> driver: "replay handler"
journal -> driver: "reuse completed result"
```

## Journaled side effects

Provider calls run inside `restate.Run()` callbacks. Restate journals the callback result. When a handler replays after a failure, completed journal entries are reused instead of blindly restarting the entire handler from the beginning.

This is why error classification happens inside the callback. A transient AWS failure must reach Restate as retryable. A terminal provider response must be marked terminal before Restate decides what to do next.

## Per-resource serialization

Each resource instance is a Virtual Object key. Exclusive lifecycle handlers for one key execute serially, which prevents overlapping provision, delete, and reconcile mutations without an external lock service.

## Durable timers

Drivers schedule future reconciliation with durable timers. A process restart does not erase the next check. No separate cron service or controller work queue is required.

## Durable does not mean magical

AWS APIs have their own idempotency and consistency behavior. Praxis drivers still need stable client tokens, careful read-after-write handling, correct provider identifiers, and explicit error classification. Restate preserves workflow progress; the driver remains responsible for using each AWS API safely.

:::tip[Full reference]
For workflow journals, deployment state, scheduling, retries, cancellation, rollback, and crash recovery, see [Orchestration in the GitHub documentation](https://github.com/shirvan/praxis/blob/main/docs/ORCHESTRATOR.md).
:::
