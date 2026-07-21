---
title: Events and observation
description: Follow deployments, resource transitions, drift, and operator decisions.
sidebar:
  order: 5
---

Praxis emits CloudEvents for deployment and resource lifecycle changes. Events make asynchronous progress inspectable without reading service logs.

Follow a deployment:

```bash
praxis observe Deployment/payments-prod
```

List retained events:

```bash
praxis list events Deployment/payments-prod
praxis list events Deployment/payments-prod -o json
```

## Event families

- Deployment submission, start, completion, failure, and cancellation
- Resource dispatch, readiness, error, replacement, and deletion
- Drift detection, correction, and external deletion
- Approval requests and operator decisions
- Notification delivery and retention events

## Notification sinks

Operators can register webhook sinks and test delivery before relying on them for incident or audit workflows.

```bash
praxis create sink \
  --name production-alerts \
  --type webhook \
  --url https://example.internal/praxis
praxis test sink/production-alerts
```

Use `-o json` when an agent or automation system consumes events. Human table output and machine output come from the same CLI command surface.

:::tip[Full reference]
For the complete event type catalog, CloudEvent fields, filtering, retention, and sink delivery behavior, see [Events in the GitHub documentation](https://github.com/shirvan/praxis/blob/main/docs/EVENTS.md).
:::
