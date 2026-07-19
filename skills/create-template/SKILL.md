# Create Template

**Description**: Author CUE templates for declaring Praxis infrastructure.

**When to Use**: Writing new CUE templates, modifying existing templates, or helping users understand template syntax.

**Prerequisites**:
- Read [docs/TEMPLATES.md](../../docs/TEMPLATES.md) for the template system
- Know which resource kinds are available (see [docs/DRIVERS.md](../../docs/DRIVERS.md))

---

## Steps

### 1. Define Variables

Variables are user inputs supplied at deploy time:

```cue
variables: {
    env:    string & ("dev" | "staging" | "prod")    // required enum
    region: string | *"us-east-1"                     // optional with default
    name:   string                                     // required string
    count:  int & >=1 & <=10 | *1                     // constrained with default
}
```

**Type reference**: `string`, `bool`, `int`, `float`, `[...string]` (list), `{[string]: string}` (struct).

### 2. Define Resources

Each resource needs: `apiVersion`, `kind`, `metadata.name`, and `spec`:

```cue
resources: {
    myVpc: {
        apiVersion: "praxis.io/alpha"
        kind:       "VPC"
        metadata: name: "\(variables.name)-vpc"
        spec: {
            region:    variables.region
            cidrBlock: "10.0.0.0/16"
            tags: {
                Environment: variables.env
                ManagedBy:   "praxis"
            }
        }
    }
}
```

**Key conventions**:
- Resource key (e.g., `myVpc`) is the logical name used for expressions
- `metadata.name` becomes part of the resource's Virtual Object key
- CUE interpolation `\(variables.field)` resolves at evaluation time

### 3. Add Cross-Resource Dependencies

Use output expressions to create DAG edges:

```cue
resources: {
    myVpc: {
        kind: "VPC"
        // ...
    }
    
    mySubnet: {
        apiVersion: "praxis.io/alpha"
        kind:       "Subnet"
        metadata: name: "\(variables.name)-subnet"
        spec: {
            region:           variables.region
            vpcId:            "${resources.myVpc.outputs.vpcId}"        // dependency
            cidrBlock:        "10.0.1.0/24"
            availabilityZone: "\(variables.region)a"
        }
    }
    
    mySg: {
        apiVersion: "praxis.io/alpha"
        kind:       "SecurityGroup"
        metadata: name: "\(variables.name)-sg"
        spec: {
            region:    variables.region
            vpcId:     "${resources.myVpc.outputs.vpcId}"              // same dependency
            groupName: "\(variables.name)-sg"
            // ...
        }
    }
}
```

**Rules**:
- `${resources.NAME.outputs.FIELD}` must occupy a full JSON value
- No partial string embedding: `"prefix-${...}"` is invalid
- Self-references are invalid
- Circular dependencies are rejected

### 4. Add Data Sources (Optional)

Look up existing resources:

```cue
data: {
    existingVpc: {
        kind:   "VPC"
        region: variables.region
        filter: { name: "shared-vpc" }    // by name
    }
}

resources: {
    mySg: {
        kind: "SecurityGroup"
        spec: {
            vpcId: "${data.existingVpc.outputs.vpcId}"  // resolved at compile time
        }
    }
}
```

Filter types: `id` (by AWS ID), `name` (by name/tag), `tag` (by key=value).

Supported data source kinds: VPC, Subnet, SecurityGroup, S3Bucket, IAMRole, Route53HostedZone.

### 5. Add Lifecycle Rules (Optional)

```cue
resources: {
    myDb: {
        kind: "RDSInstance"
        lifecycle: {
            preventDestroy: true                          // block deletion
            ignoreChanges: ["spec.tags.LastUpdated"]       // skip drift for these
            timeouts: {
                create: "15m"                              // override default 5m
                delete: "10m"
            }
        }
        spec: { /* ... */ }
    }
}
```

### 6. Use CUE Advanced Features (Optional)

**Comprehensions** — generate multiple resources:
```cue
for i, zone in ["a", "b", "c"] {
    "subnet-\(zone)": {
        kind: "Subnet"
        metadata: name: "\(variables.name)-\(zone)"
        spec: {
            cidrBlock:        "10.0.\(i).0/24"
            availabilityZone: "\(variables.region)\(zone)"
        }
    }
}
```

**Conditionals** — include resources based on variables:
```cue
if variables.env == "prod" {
    monitoring: {
        kind: "MetricAlarm"
        // only created for prod
    }
}
```

**Definitions** — reusable schemas:
```cue
#StandardTags: {
    Environment: variables.env
    ManagedBy:   "praxis"
    Team:        "platform"
}

resources: {
    myBucket: {
        kind: "S3Bucket"
        spec: tags: #StandardTags & { Purpose: "data" }
    }
}
```

**Hidden fields** — internal computation:
```cue
_cidr_base: "10.0"

resources: {
    myVpc: { spec: cidrBlock: "\(_cidr_base).0.0/16" }
}
```

---

## Complete Example

```cue
variables: {
    env:    string & ("dev" | "staging" | "prod")
    region: string | *"us-east-1"
    name:   string
}

#Tags: {
    Environment: variables.env
    ManagedBy:   "praxis"
}

resources: {
    vpc: {
        apiVersion: "praxis.io/alpha"
        kind:       "VPC"
        metadata: name: "\(variables.name)-vpc"
        spec: {
            region:    variables.region
            cidrBlock: "10.0.0.0/16"
            enableDnsSupport:   true
            enableDnsHostnames: true
            tags: #Tags
        }
    }

    publicSubnet: {
        apiVersion: "praxis.io/alpha"
        kind:       "Subnet"
        metadata: name: "\(variables.name)-public"
        spec: {
            region:              variables.region
            vpcId:               "${resources.vpc.outputs.vpcId}"
            cidrBlock:           "10.0.1.0/24"
            availabilityZone:    "\(variables.region)a"
            mapPublicIpOnLaunch: true
            tags:                #Tags
        }
    }

    webSg: {
        apiVersion: "praxis.io/alpha"
        kind:       "SecurityGroup"
        metadata: name: "\(variables.name)-web-sg"
        spec: {
            region:      variables.region
            vpcId:       "${resources.vpc.outputs.vpcId}"
            groupName:   "\(variables.name)-web-sg"
            description: "Web server security group"
            ingressRules: [{
                protocol:   "tcp"
                fromPort:   443
                toPort:     443
                cidrBlocks: ["0.0.0.0/0"]
            }]
            tags: #Tags
        }
    }

    if variables.env == "prod" {
        dbSubnetGroup: {
            apiVersion: "praxis.io/alpha"
            kind:       "DBSubnetGroup"
            lifecycle: preventDestroy: true
            metadata: name: "\(variables.name)-db-subnets"
            spec: {
                region: variables.region
                // ...
            }
        }
    }
}
```

Deploy: `praxis deploy -f template.cue -v env=prod -v name=myapp`

---

## Verification

1. **Format**: `praxis fmt template.cue`
2. **Validate**: `praxis plan -f template.cue -v env=dev -v name=test --dry-run`
3. **Check CUE syntax**: `cue vet template.cue` (if CUE CLI installed)

## Common Pitfalls

1. **Partial expression in string**: `"sg-${...}"` is invalid — expressions must be full values
2. **Missing required variable**: All variables without `*default` are required at deploy time
3. **Self-referencing resource**: `${resources.myVpc.outputs.vpcId}` inside `myVpc` is rejected
4. **Wrong output field name**: Check driver's Outputs struct for available fields
5. **CUE interpolation vs expression**: `\(variables.x)` resolves at CUE time, `${resources.x.outputs.y}` at dispatch time
6. **Missing region in spec**: Most resources require explicit `region` field

## Available Resource Kinds

See [docs/DRIVERS.md](../../docs/DRIVERS.md) for the full list of 51 supported kinds and their schemas in `schemas/aws/`.
