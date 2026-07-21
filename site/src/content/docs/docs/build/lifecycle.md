---
title: Lifecycle controls
description: Control resource ownership, drift correction, deletion, and replacement.
sidebar:
  order: 4
---

Lifecycle policy lets a template state how Praxis should treat a resource after provisioning.

```cue
resources: database: {
  apiVersion: "praxis.io/alpha"
  kind: "RDSInstance"
  metadata: {name: "payments-primary", labels: {}}
  spec: {
    // Resource fields omitted for clarity.
  }
  lifecycle: {
    preventDestroy: true
    managedDriftCorrection: true
    ignoreChanges: ["spec.tags.external-controller"]
  }
}
```

## `preventDestroy`

Blocks a delete or replacement that would destroy the external resource. Praxis reports the policy conflict instead of silently bypassing it.

## `managedDriftCorrection`

Controls whether reconciliation writes changes back to AWS.

- `true`: detected drift is corrected toward the declared specification.
- `false`: detected drift is reported without writes. The resource remains healthy under the requested policy and carries drift context.

## `ignoreChanges`

Excludes selected fields from desired-versus-observed comparison. Use it when another controller legitimately owns a field. Avoid broad ignore rules because they reduce the state Praxis can verify.

## Replacement

Some AWS fields are immutable. A plan reports when a change requires replacement. The user can explicitly target replacement or allow it for the deployment. Praxis does not infer permission to destroy protected infrastructure.

:::tip[Full reference]
For lifecycle state, conditions, drift policy, import modes, replacement, and driver hooks, see [Generic drivers in the GitHub documentation](https://github.com/shirvan/praxis/blob/main/docs/GENERIC_DRIVERS.md).
:::
