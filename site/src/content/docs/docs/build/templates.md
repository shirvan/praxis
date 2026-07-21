---
title: CUE templates
description: Define typed, validated infrastructure compositions in CUE.
sidebar:
  order: 1
---

Praxis templates are CUE values with optional `variables` and `data` sections plus a required `resources` section. CUE provides types, constraints, defaults, composition, and validation before any provider call is made.

```cue
variables: {
  application: string & =~"^[a-z][a-z0-9-]{2,30}$"
  environment: "dev" | "staging" | "prod"
  region: string | *"us-west-2"
}

resources: archive: {
  apiVersion: "praxis.io/alpha"
  kind: "S3Bucket"
  metadata: {
    name: "\(variables.application)-\(variables.environment)-archive"
    labels: team: "payments"
  }
  spec: {
    region: variables.region
    versioning: true
    encryption: {
      enabled: true
      algorithm: "AES256"
    }
    tags: {
      application: variables.application
      environment: variables.environment
    }
  }
}
```

## Resource shape

Every resource uses the same outer envelope:

| Field | Purpose |
| --- | --- |
| `apiVersion` | The current contract, `praxis.io/alpha` |
| `kind` | A registered Praxis resource kind |
| `metadata.name` | Stable logical name used by the template and driver |
| `metadata.labels` | Praxis metadata for organization and selection |
| `spec` | Provider-specific desired state validated by the resource schema |
| `lifecycle` | Optional ownership, drift, and deletion policy |

## Variables and values

Supply variables with repeated flags or a JSON values file:

```bash
praxis plan stack.cue \
  --account prod \
  --var application=payments \
  --var environment=prod

praxis plan stack.cue --account prod -f prod.vars.json
```

CUE validation runs before the plan. Missing variables, invalid enums, and constraint violations fail without touching AWS.

## Discover schemas

```bash
praxis list schemas
praxis get schema S3Bucket
praxis get schema S3Bucket -o json
```

The CLI embeds the CUE schemas, so discovery does not require a running Praxis environment.

:::caution[One alpha contract]
Praxis does not maintain parallel alpha schemas or backwards-compatible template readers. Keep templates aligned with the Praxis revision that executes them.
:::

:::tip[Full reference]
For template structure, variables, expressions, validation, examples, and migration guidance, see [Templates in the GitHub documentation](https://github.com/shirvan/praxis/blob/main/docs/TEMPLATES.md).
:::
