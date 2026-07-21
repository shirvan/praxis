---
title: Data sources
description: Read existing AWS resources without taking lifecycle ownership.
sidebar:
  order: 3
---

A `data` block looks up an existing AWS resource and exposes its outputs to managed resources. The lookup is read-only. It stores no resource state and establishes no reconciliation lifecycle.

```cue
data: sharedNetwork: {
  kind: "VPC"
  region: "us-west-2"
  filter: tag: {
    Name: "shared-services"
    Environment: "prod"
  }
}

resources: appSubnet: {
  apiVersion: "praxis.io/alpha"
  kind: "Subnet"
  metadata: {name: "payments-a", labels: {}}
  spec: {
    region: "us-west-2"
    vpcId: "${data.sharedNetwork.outputs.vpcId}"
    cidrBlock: "10.60.10.0/24"
    availabilityZone: "us-west-2a"
    tags: {}
  }
}
```

## Filters

The current filter contract accepts:

```cue
filter: {
  id?: string
  name?: string
  tag?: [string]: string
}
```

Supported filters depend on the AWS resource. All 51 resource kinds use the generic lookup contract. Common examples include:

- `VPC`
- `Subnet`
- `SecurityGroup`
- `EC2Instance`
- `LambdaFunction`
- `RDSInstance`
- `DynamoDBTable`
- `ECRRepository`
- `ECSCluster`
- `LogGroup`
- `S3Bucket`
- `IAMRole`
- `Route53HostedZone`

Composite resources may require their documented import identity in `filter.id`. Secret and SSM parameter lookups use metadata-only AWS calls and never retrieve their values.

Use the [resource catalog](/resources/) to review each resource’s configuration, outputs, lookup shape, examples, and complete CUE schema.

## Data source or import?

| Use | Data source | Import |
| --- | --- | --- |
| Read outputs from existing infrastructure | Yes | Yes |
| Persist resource state | No | Yes |
| Detect drift later | No | Yes |
| Correct drift | No | Managed import only |
| Delete with a deployment | No | Depends on ownership mode |

Use a data source for a dependency owned elsewhere. Use import when Praxis should remember and observe or manage the resource.

:::tip[Full reference]
For the generic lookup contract, identity resolution, ambiguity rules, and provider adapter behavior, see [Generic drivers in the GitHub documentation](https://github.com/shirvan/praxis/blob/main/docs/GENERIC_DRIVERS.md).
:::
