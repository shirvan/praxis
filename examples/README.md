# Examples

Real-world Praxis template examples organized by AWS domain.

## Directory Structure

```
examples/
├── ec2/          EC2 instances, key pairs, EBS volumes
├── vpc/          VPCs, subnets, gateways, route tables, peering
├── s3/           S3 buckets
└── stacks/       Multi-resource compositions (cross-domain)
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
| `multi-az-vpc` | Production VPC: 2-AZ public/private subnets, IGW, NAT, route tables | VPC → IGW → 4× Subnet → ElasticIP → NATGateway → 2× RouteTable |
| `vpc-peering` | Two peered VPCs with cross-VPC routing | 2× VPC → VPCPeering → 2× Subnet → 2× RouteTable |

### S3 — `examples/s3/`

| Template | Description | Resources |
|----------|-------------|-----------|
| `app-buckets` | Assets, logs, and backup buckets | 3× S3Bucket |
| `static-website` | S3 bucket for static site content | S3Bucket |

### Stacks — `examples/stacks/`

| Template | Description | Resources |
|----------|-------------|-----------|
| `ec2-web-stack` | VPC + security group + EC2 instance | VPC → SecurityGroup → EC2Instance |
| `three-tier-app` | Full three-tier: VPC, subnets, IGW, NAT, security groups, web + app servers, S3 | 13 resources |
| `network-locked-app` | Defense-in-depth: VPC + NACL + SG + EC2 | VPC → Subnet → NetworkACL + SecurityGroup → EC2Instance |

## Variables

Each `.cue` template has a matching `.vars.json` file with sample variable values. Common variables:

- **`name`** — Application name (lowercase, alphanumeric + hyphens)
- **`environment`** — `dev`, `staging`, or `prod` (controls monitoring, instance sizes, etc.)
- **`vpcId`** / **`subnetId`** — Pre-existing resource IDs (when not created by the template)

## Output Expressions

Templates use `${resources.<name>.outputs.<field>}` to wire resources together. The orchestrator builds a DAG from these expressions and provisions resources in dependency order, resolving outputs at dispatch time.
