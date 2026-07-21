---
title: Dependencies and outputs
description: Connect resources and let Praxis schedule them in dependency order.
sidebar:
  order: 2
---

Use an output expression when one resource needs a value created by another:

```cue
resources: {
  network: {
    apiVersion: "praxis.io/alpha"
    kind: "VPC"
    metadata: {name: "payments", labels: {}}
    spec: {
      region: "us-west-2"
      cidrBlock: "10.42.0.0/16"
      tags: {}
    }
  }

  appSubnet: {
    apiVersion: "praxis.io/alpha"
    kind: "Subnet"
    metadata: {name: "app-a", labels: {}}
    spec: {
      region: "us-west-2"
      vpcId: "${resources.network.outputs.vpcId}"
      cidrBlock: "10.42.10.0/24"
      availabilityZone: "us-west-2a"
      tags: {}
    }
  }
}
```

The expression establishes a graph edge from `network` to `appSubnet`. Praxis provisions the VPC first, reads its `vpcId` output, injects the typed value into the subnet specification, and then dispatches the subnet.

```d2
direction: down

vpc: "network\nVPC"
subnet: "appSubnet\nSubnet"
parallel: "other independent resources" {
  shape: document
}

vpc -> subnet: "outputs.vpcId\nhydrates spec.vpcId"
parallel.style.stroke-dash: 4
```

## Expression form

```text
${resources.<resource-name>.outputs.<field>}
```

An expression must occupy the entire string value. This lets Praxis preserve the output type instead of performing text substitution.

## Scheduling

Praxis builds a directed acyclic graph from all output expressions. Resources with no unresolved dependencies can run concurrently. A failed resource prevents its transitive dependents from running and records the causal chain in the deployment result.

```d2
direction: down

vpc: VPC
subnet_a: "Subnet A"
subnet_b: "Subnet B"
sg: "Security group"
instance_a: "Instance A"
instance_b: "Instance B"
assets: "S3 assets"

vpc -> subnet_a
vpc -> subnet_b
vpc -> sg
subnet_a -> instance_a
sg -> instance_a
subnet_b -> instance_b
sg -> instance_b
instance_a -> assets: "instance ID tag"
```

Cycles and references to unknown resources or outputs fail during evaluation, before provisioning begins.

:::tip[Full reference]
For graph construction, scheduling, failure propagation, replacement, and rollback internals, see [Orchestration in the GitHub documentation](https://github.com/shirvan/praxis/blob/main/docs/ORCHESTRATOR.md).
:::
