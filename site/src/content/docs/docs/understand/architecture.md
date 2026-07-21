---
title: Architecture
description: How Praxis combines CUE, durable orchestration, and uniform AWS resource drivers.
sidebar:
  order: 1
---

Praxis separates declaration, orchestration, durable execution, and provider behavior into clear layers.

```d2
direction: down

template: "CUE template" {
  shape: document
}
core: "Praxis Core\nvalidate · plan · build the DAG · coordinate"
restate: "Restate\ndurable calls · state · timers · serialization"
drivers: "Driver packs\none lifecycle contract · 51 AWS resource kinds"
aws: "AWS APIs" {
  shape: cloud
}

template -> core: "declared resources"
core -> restate: "durable workflows and RPC"
restate -> drivers: "keyed resource operations"
drivers -> aws: "observe and converge"
```

## Praxis Core

Core receives commands, evaluates templates, resolves data sources and secrets, builds the resource graph, and coordinates deployments. It owns relationships between resources. Individual drivers do not know about the deployment DAG.

## Resources as Virtual Objects

Each managed resource is a keyed Restate Virtual Object. Mutation handlers are exclusive, so concurrent requests for the same resource are serialized without a separate distributed lock. Shared handlers expose status, outputs, and inputs concurrently.

## Uniform drivers

Every production driver uses the same generic lifecycle kernel with resource-specific hooks for:

- Spec decoding and provider identity
- AWS client construction
- Create, read, update, and delete operations
- Desired-versus-observed field comparison
- Output normalization
- Provider error classification

The service-specific code describes AWS behavior. The generic kernel owns state transitions, conditions, reconciliation policy, and handler shape.

## Driver packs

Drivers are grouped into storage, network, compute, identity, and monitoring services. The grouping controls deployment and scaling boundaries without changing the per-resource contract.

## No required cluster

Restate provides the durable runtime capabilities that Praxis needs directly. Kubernetes is therefore optional infrastructure for hosting the services, not part of the Praxis resource model.

:::tip[Full reference]
For service boundaries, state ownership, request flows, deployment topology, and implementation details, see the [architecture blueprint on GitHub](https://github.com/shirvan/praxis/blob/main/docs/PRAXIS_ARCHITECTURE.md).
:::
