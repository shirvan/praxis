---
title: Desired and observed state
description: The state model behind planning, importing, and reconciliation.
sidebar:
  order: 2
---

Praxis reasons about infrastructure through three related views.

```d2
direction: down

desired: "Desired state\nevaluated CUE"
observe: "Observe\nread AWS"
actual: "Observed state\nnormalized provider data"
compare: "Compare"
policy: "Lifecycle policy" {
  auto: "auto"
  report: "observe"
}
write: "Correct AWS"
condition: "Ready + DriftFree condition"

desired -> compare
actual -> compare
observe -> actual
compare -> policy: "difference"
policy.auto -> write: "drifted"
policy.report -> condition: "report only"
write -> observe: "read again"
compare -> condition: "matching"
```

## Desired state

Desired state is the validated resource specification produced from the CUE template. It records what the user asked Praxis to maintain.

## Observed state

Observed state is a fresh description returned by the AWS API. Drivers normalize provider responses before comparing them so irrelevant ordering and provider defaults do not create false drift.

## Outputs

Outputs are stable values exposed to users and dependent resources, such as a VPC ID, bucket ARN, or load balancer DNS name. They are persisted with resource state and can hydrate downstream specifications.

## Planning

Planning combines desired state with stored outputs and a live provider probe when enough identity is known. The result distinguishes:

- A resource that does not exist and must be created
- A matching resource that needs no changes
- A mutable field change
- An immutable change that requires replacement
- A resource removed from the desired deployment

## Reconciliation

Reconciliation repeats the desired-versus-observed comparison after deployment. Policy determines whether the result produces corrective writes or a visibility-only drift condition.

This separation lets Praxis report a healthy control-plane decision without pretending AWS matches the declaration. For example, correction-disabled drift can remain `Ready` while `DriftFree=False` preserves the fact that AWS differs.

:::tip[Full reference]
For desired and observed state, ownership modes, field comparison, conditions, and correction policy, see [Generic drivers in the GitHub documentation](https://github.com/shirvan/praxis/blob/main/docs/GENERIC_DRIVERS.md).
:::
