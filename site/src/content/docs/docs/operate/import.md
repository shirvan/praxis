---
title: Import existing resources
description: Adopt AWS infrastructure into managed or observed Praxis state.
sidebar:
  order: 2
---

Import creates Praxis state for an AWS resource that already exists. It does not recreate the external resource.

## Choose ownership

- **Managed import** adopts the current resource and lets future desired state and reconciliation change it.
- **Observed import** tracks status and drift but does not write corrective changes.

Use observed mode first when ownership is unclear or another system may still manage the resource.

## Import a resource

```bash
praxis import VPC \
  --id vpc-0123456789abcdef0 \
  --account prod \
  --region us-west-2 \
  --observe
```

The exact provider identifier depends on the resource kind. Region-scoped resources require a region. Composite resources may use more than one provider identity component.

After import:

```bash
praxis get VPC/us-west-2~vpc-0123456789abcdef0
praxis reconcile VPC/us-west-2~vpc-0123456789abcdef0
```

## Import is not lookup

Import persists a driver resource with status, outputs, conditions, and future reconciliation. A [data source](/docs/build/data-sources/) performs an ephemeral read during template evaluation and disappears after returning outputs.

:::caution
Confirm that the imported resource is no longer being changed by another infrastructure controller before enabling managed drift correction. Two control planes with the same field ownership can continually overwrite each other.
:::

:::tip[Full reference]
For observed and managed ownership, import identity, stored state, deletion rules, and reconciliation behavior, see [Generic drivers in the GitHub documentation](https://github.com/shirvan/praxis/blob/main/docs/GENERIC_DRIVERS.md).
:::
