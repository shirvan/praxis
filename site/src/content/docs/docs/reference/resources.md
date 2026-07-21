---
title: AWS resources
description: Browse the resource kinds and schemas currently supported by Praxis.
sidebar:
  order: 1
---

Praxis currently ships 51 AWS resource drivers. They share one generic lifecycle contract for provisioning, planning, importing, observing, deleting, lookup, and reconciliation.

[Open the searchable resource catalog](/resources/)

The catalog supports both card and compact list views. Search by resource kind, AWS service, or description, then filter the result by infrastructure domain.

Each resource page is written for human review and includes:

- A representative CUE resource definition
- Links to complete examples that use the resource
- Every `spec` field, including whether it is required, its default, and its exact CUE constraint
- Every observed output available to dependent resources
- Data-source lookup and import examples
- The complete current-alpha CUE schema

The field and output guides are derived from the schemas during the site build. A schema change therefore updates the public reference instead of requiring a second contract to be maintained by hand.

## Discover from the CLI

```bash
praxis list schemas
praxis get schema VPC
praxis get schema VPC -o json
```

The embedded CUE schema is the current source of truth for fields, validation constraints, defaults, and output names.

## Capability labels

- **Lifecycle** means the resource participates in the uniform managed-resource lifecycle.
- **Drift** means the driver observes live provider state and compares it with desired state.
- **Lookup** means the resource can currently be used in a read-only template `data` block.

All 51 production resource drivers support lookup through the same generic driver contract.

:::tip[Full reference]
For driver responsibilities, package layout, handler behavior, and implementation guidance, see [Drivers in the GitHub documentation](https://github.com/shirvan/praxis/blob/main/docs/DRIVERS.md). The linked CUE schema on each resource page remains the field-level source of truth.
:::
