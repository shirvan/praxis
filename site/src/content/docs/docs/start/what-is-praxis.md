---
title: What is Praxis?
description: Understand the problem Praxis solves and its core operating model.
sidebar:
  order: 1
---

Praxis is a durable AWS infrastructure control plane. You declare resources in typed CUE templates, inspect a plan, and submit the deployment. Praxis then continues observing those resources instead of forgetting them when the initial operation finishes.

## What Praxis owns

Praxis manages the full resource lifecycle:

- Validate resource definitions and organizational constraints with CUE.
- Resolve dependencies between resources from output expressions.
- Compare the planned specification with existing Praxis and AWS state.
- Provision independent resources concurrently in dependency order.
- Import existing AWS resources without recreating them.
- Detect and report configuration drift continuously.
- Correct drift when managed reconciliation is enabled.
- Persist status, outputs, conditions, and an event history.

## What runs

A Praxis installation contains:

- **Praxis Core**, which evaluates templates, builds the resource graph, and coordinates deployments.
- **Driver packs**, which implement one uniform lifecycle for 51 AWS resource kinds.
- **Restate**, which stores durable execution progress and resource state.
- **The Praxis CLI or HTTP clients**, which issue commands through Restate ingress.

There is no Kubernetes dependency. Praxis can run in Docker Compose, directly on compute, or alongside an external Restate environment. Running it on Kubernetes is an optional deployment choice, not a product requirement.

## Managed and observed resources

Managed resources are expected to match the declared state. Praxis can correct their drift. Observed resources are visibility-only: external changes are reported but not overwritten.

When automatic correction is disabled, a resource can remain operationally healthy while carrying a clear `Drifted` condition. That represents the requested policy accurately: the resource is behaving as configured, and the external difference remains visible.

:::tip[Full reference]
For service boundaries, execution flows, state ownership, and deployment topology, see the [Praxis architecture blueprint on GitHub](https://github.com/shirvan/praxis/blob/main/docs/PRAXIS_ARCHITECTURE.md).
:::
