# Examples

Real-world Praxis template examples organized by AWS domain.

## Directory Structure

```
examples/
├── acm/          ACM certificates, DNS validation, HTTPS stacks
├── ec2/          EC2 instances, key pairs, EBS volumes
├── vpc/          VPCs, subnets, gateways, route tables, peering
├── s3/           S3 buckets
├── stacks/       Multi-resource compositions (cross-domain)
├── lifecycle/    Lifecycle rules (preventDestroy, ignoreChanges)
├── policies/     Policy-as-code constraints (security, cost, network)
└── ops/          Platform deployment (Kubernetes manifests, autoscaling)
```

## Quick Start

```bash
# 1. Register a template
praxis template register examples/ec2/dev-instance.cue --description "Dev EC2 instance"

# 2. Preview (dry-run)
praxis deploy dev-instance --account local -f examples/ec2/dev-instance.vars.json --dry-run

# 3. Deploy
praxis deploy dev-instance --account local -f examples/ec2/dev-instance.vars.json --key myapp-dev --wait
```

## Examples

### EC2 — `examples/ec2/`

| Template | Description | Resources |
|----------|-------------|-----------|
| `dev-instance` | Minimal dev EC2 instance | EC2Instance |
| `ec2-instance` | Standalone EC2 with configurable root volume | EC2Instance |
| `bastion-host` | SSH jump box with key pair and security group | KeyPair → SecurityGroup → EC2Instance |
| `web-fleet` | Two web servers across AZs with shared EBS | SecurityGroup → 2× EC2Instance + EBSVolume |
| `ebs-data-tier` | High-performance EBS volumes (io2 + gp3) | 2× EBSVolume |

### VPC — `examples/vpc/`

| Template | Description | Resources |
|----------|-------------|-----------|
| `basic-vpc` | Simple VPC with DNS support | VPC |
| `multi-az-vpc` | Production VPC: 2-AZ public/private subnets, IGW, NAT, route tables | VPC -> IGW -> 4x Subnet -> ElasticIP -> NATGateway -> 2x RouteTable |
| `vpc-peering` | Two peered VPCs with cross-VPC routing | 2x VPC -> VPCPeering -> 2x Subnet -> 2x RouteTable |
| `dynamic-subnets` | Generate N subnets from a struct list variable | VPC -> Nx Subnet (comprehension) |

### ACM — `examples/acm/`

| Template | Description | Resources |
|----------|-------------|-----------|
| `basic-certificate` | DNS-validated certificate for a single domain | ACMCertificate |
| `wildcard-certificate` | Wildcard certificate (*.domain) with ECDSA and apex SAN | ACMCertificate |
| `https-stack` | Full HTTPS flow: certificate + Route 53 validation + ALB listener | ACMCertificate → DNSRecord + Listener |

### S3 — `examples/s3/`

| Template | Description | Resources |
|----------|-------------|-----------|
| `app-buckets` | Assets, logs, and backup buckets | 3x S3Bucket |
| `static-website` | S3 bucket for static site content | S3Bucket |
| `dynamic-buckets` | Generate N buckets from a list variable + optional logging bucket | Nx S3Bucket (comprehension) |

### Stacks — `examples/stacks/`

| Template | Description | Resources |
|----------|-------------|-----------|
| `ec2-web-stack` | VPC + security group + EC2 instance | VPC → SecurityGroup → EC2Instance |
| `three-tier-app` | Full three-tier: VPC, subnets, IGW, NAT, security groups, web + app servers, S3 (with lifecycle rules) | 13 resources |
| `network-locked-app` | Defense-in-depth: VPC + NACL + SG + EC2 | VPC → Subnet → NetworkACL + SecurityGroup → EC2Instance |

### Lifecycle Rules — `examples/lifecycle/`

Protective rules for controlling delete and drift behavior.

| Template | Description | Features |
|----------|-------------|----------|
| `protected-db` | RDS instance with deletion protection | `preventDestroy` — blocks `praxis delete` until explicitly unlocked via `allowDelete` variable |
| `external-managed` | S3 bucket co-managed with external tools | `ignoreChanges` — lets billing, compliance, and other systems manage specific tags |

```bash
# Deploy a protected database
praxis deploy protected-db --account local -f examples/lifecycle/protected-db.vars.json --key mydb --wait

# Attempt delete (fails: lifecycle.preventDestroy enabled)
praxis delete Deployment/mydb --yes --wait

# Remove protection and retry
praxis deploy protected-db --account local -f examples/lifecycle/protected-db.vars.json \
  --var allowDelete=true --key mydb --wait
praxis delete Deployment/mydb --yes --wait
```

### Policies — `examples/policies/`

Policies are CUE constraint files that enforce organizational rules across templates. They are applied during template evaluation — the engine unifies each policy with the template and reports violations as `PolicyViolation` errors.

| Policy | Description | Enforces |
|--------|-------------|----------|
| `security-baseline` | Organization-wide security defaults | Encryption on S3 + EC2 root volumes, required `environment`/`app` tags |
| `prod-guardrails` | Production environment guardrails | Monitoring on EC2, private + versioned S3, DNS on VPCs, `preventDestroy` on prod resources |
| `cost-controls` | Cost control limits | Approved EC2 instance types, root volume ≤500 GiB, no provisioned IOPS |
| `network-hardening` | Network security hardening | Private-only S3 buckets, DNS support on all VPCs |

#### Policy patterns

Policies use CUE's pattern constraint syntax to target resources:

```cue
// Apply to ALL resources — universal constraint
resources: [_]: spec: tags: { environment: string }

// Apply to resources matching a name pattern
resources: [=~"-prod"]: spec: monitoring: true

// Apply conditionally by resource kind
resources: [_]: {
    kind: string
    if kind == "S3Bucket" {
        spec: encryption: enabled: true
    }
}
```

#### Usage

```bash
# Add a global policy (applies to all templates)
praxis policy add --name security-baseline --scope global \
  --source examples/policies/security-baseline.cue

# Add a template-scoped policy (applies only to one template)
praxis policy add --name prod-guardrails --scope template --template my-app \
  --source examples/policies/prod-guardrails.cue

# Validate a template against all active policies
praxis template validate examples/ec2/ec2-instance.cue \
  -f examples/ec2/ec2-instance.vars.json
```

## Variables

Each `.cue` template has a matching `.vars.json` file with sample variable values. Common variables:

- **`name`** — Application name (lowercase, alphanumeric + hyphens)
- **`environment`** — `dev`, `staging`, or `prod` (controls monitoring, instance sizes, etc.)
- **`vpcId`** / **`subnetId`** — Pre-existing resource IDs (when not created by the template)

## Output Expressions

Templates use `${resources.<name>.outputs.<field>}` to wire resources together. The orchestrator builds a DAG from these expressions and provisions resources in dependency order, resolving outputs at dispatch time.

### Ops — `examples/ops/`

Kubernetes manifests for deploying the Praxis platform itself.

| Manifest | Description | Resources |
|----------|-------------|-----------|
| `k8s/praxis-full` | Full Praxis stack on K8s (Restate + Core + all driver packs) | Namespace, ConfigMap, StatefulSet, 5× Deployment + Service |
| `k8s/praxis-autoscaling` | HPA configs to scale driver packs based on CPU demand | 4× HorizontalPodAutoscaler (network 1–8, compute 1–6, storage 1–4, iam 1–3) |

See the [Operators Guide](../docs/OPERATORS.md) for full deployment instructions.
