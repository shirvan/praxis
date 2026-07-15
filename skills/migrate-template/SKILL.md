# Skill: Migrate Terraform / CloudFormation / Crossplane to Praxis CUE

> Convert existing IaC (Terraform HCL, CloudFormation YAML/JSON, Crossplane manifests)
> into a Praxis CUE template, then verify it with a dry-run plan.

## When to Use

The user has existing infrastructure-as-code and wants an equivalent Praxis template.

## Procedure

### 1. Detect the source format

Heuristics (in priority order):

| Format | Signal |
|--------|--------|
| Terraform | `resource "aws_..." "name" {` blocks |
| CloudFormation | `Type: AWS::...` (YAML) or `"Type": "AWS::..."` (JSON) |
| Crossplane | `kind: <Kind>` together with `apiVersion:` |

### 2. Build an inventory

Scan the source files and extract every resource type:

- Terraform: regex `resource\s+"([^"]+)"\s+"[^"]+"`
- CloudFormation YAML: regex `^\s*Type:\s+(AWS::\S+)`
- CloudFormation JSON: regex `"Type"\s*:\s*"(AWS::\S+)"`
- Crossplane: regex `^kind:\s+(\S+)`

For each type, look it up in the mapping table below. Track:
- **Mapped kinds** — types with a Praxis equivalent
- **Unmapped types** — no Praxis equivalent; these must be flagged, not silently dropped

### 3. Look up Praxis schemas for the mapped kinds

For each mapped kind, read its schema to learn valid spec fields:
- `schemas/aws/{resource}/{resource}.cue` in this repo, or
- `praxis schema show <Kind>` if the CLI is available.

### 4. Generate the CUE template

Rules:

1. Each source resource maps to one entry in the `resources:` block.
2. Use the mapping table to determine the Praxis `kind`.
3. Cross-resource references become output expressions: `${resources.<name>.outputs.<field>}`.
   These must occupy a **full JSON value** — `"vpcId": "${resources.vpc.outputs.vpcId}"` is
   valid; embedding inside a longer string is not.
4. Parameterized values (Terraform `var.*`, CFN `Parameters`) become a `variables:` block,
   referenced with CUE interpolation: `"\(variables.<name>)"`.
5. Every resource needs: `apiVersion: "praxis.io/v1"`, `kind`, `metadata.name`, `spec`
   (with `region` where the kind requires it).
6. Preserve logical resource names from the source where possible.
7. If a source feature has no Praxis equivalent, add a CUE comment:
   `// TODO: <feature> not supported`.
8. End the template with a comment block listing everything that could not be migrated
   (unmapped types, unsupported features, provider-specific behaviors).

### 5. Verify with a dry-run plan

A single plan call validates CUE syntax, schema conformance, and reference resolution:

```bash
praxis plan generated.cue --account <account> [--var k=v ...]
```

or via the API: `POST /PraxisCommandService/Plan` with `{"template": "<cue source>"}`.

If it fails, fix the reported errors and re-plan. Iterate until the plan succeeds, then
review the plan output with the user — resource counts should match the source inventory.

## Resource Type Mapping (canonical)

### Terraform → Praxis

| Terraform | Praxis Kind |
|-----------|-------------|
| `aws_s3_bucket` | `S3Bucket` |
| `aws_security_group` | `SecurityGroup` |
| `aws_vpc` | `VPC` |
| `aws_subnet` | `Subnet` |
| `aws_internet_gateway` | `InternetGateway` |
| `aws_nat_gateway` | `NATGateway` |
| `aws_route_table` | `RouteTable` |
| `aws_network_acl` | `NetworkACL` |
| `aws_eip` | `ElasticIP` |
| `aws_instance` | `EC2Instance` |
| `aws_key_pair` | `KeyPair` |
| `aws_ami` | `AMI` |
| `aws_ebs_volume` | `EBSVolume` |
| `aws_iam_role` | `IAMRole` |
| `aws_iam_policy` | `IAMPolicy` |
| `aws_iam_user` | `IAMUser` |
| `aws_iam_group` | `IAMGroup` |
| `aws_iam_instance_profile` | `IAMInstanceProfile` |
| `aws_route53_zone` | `Route53HostedZone` |
| `aws_route53_record` | `Route53Record` |
| `aws_route53_health_check` | `Route53HealthCheck` |
| `aws_db_instance` | `RDSInstance` |
| `aws_rds_cluster` | `AuroraCluster` |
| `aws_db_subnet_group` | `DBSubnetGroup` |
| `aws_db_parameter_group` | `DBParameterGroup` |
| `aws_lambda_function` | `LambdaFunction` |
| `aws_lambda_layer_version` | `LambdaLayer` |
| `aws_lambda_permission` | `LambdaPermission` |
| `aws_lambda_event_source_mapping` | `EventSourceMapping` |
| `aws_lb` | `ALB` |
| `aws_lb_target_group` | `TargetGroup` |
| `aws_lb_listener` | `Listener` |
| `aws_lb_listener_rule` | `ListenerRule` |
| `aws_vpc_peering_connection` | `VPCPeeringConnection` |
| `aws_sns_topic` | `SNSTopic` |
| `aws_sns_topic_subscription` | `SNSSubscription` |
| `aws_sqs_queue` | `SQSQueue` |
| `aws_sqs_queue_policy` | `SQSQueuePolicy` |
| `aws_ecr_repository` | `ECRRepository` |
| `aws_ecr_lifecycle_policy` | `ECRLifecyclePolicy` |
| `aws_acm_certificate` | `ACMCertificate` |
| `aws_cloudwatch_log_group` | `LogGroup` |
| `aws_cloudwatch_metric_alarm` | `MetricAlarm` |
| `aws_cloudwatch_dashboard` | `Dashboard` |

### CloudFormation → Praxis

| CloudFormation | Praxis Kind |
|----------------|-------------|
| `AWS::S3::Bucket` | `S3Bucket` |
| `AWS::EC2::SecurityGroup` | `SecurityGroup` |
| `AWS::EC2::VPC` | `VPC` |
| `AWS::EC2::Subnet` | `Subnet` |
| `AWS::EC2::InternetGateway` | `InternetGateway` |
| `AWS::EC2::NatGateway` | `NATGateway` |
| `AWS::EC2::RouteTable` | `RouteTable` |
| `AWS::EC2::NetworkAcl` | `NetworkACL` |
| `AWS::EC2::EIP` | `ElasticIP` |
| `AWS::EC2::Instance` | `EC2Instance` |
| `AWS::EC2::KeyPair` | `KeyPair` |
| `AWS::EC2::Volume` | `EBSVolume` |
| `AWS::EC2::VPCPeeringConnection` | `VPCPeeringConnection` |
| `AWS::IAM::Role` | `IAMRole` |
| `AWS::IAM::Policy` | `IAMPolicy` |
| `AWS::IAM::User` | `IAMUser` |
| `AWS::IAM::Group` | `IAMGroup` |
| `AWS::IAM::InstanceProfile` | `IAMInstanceProfile` |
| `AWS::Route53::HostedZone` | `Route53HostedZone` |
| `AWS::Route53::RecordSet` | `Route53Record` |
| `AWS::Route53::HealthCheck` | `Route53HealthCheck` |
| `AWS::RDS::DBInstance` | `RDSInstance` |
| `AWS::RDS::DBCluster` | `AuroraCluster` |
| `AWS::RDS::DBSubnetGroup` | `DBSubnetGroup` |
| `AWS::RDS::DBParameterGroup` | `DBParameterGroup` |
| `AWS::Lambda::Function` | `LambdaFunction` |
| `AWS::Lambda::LayerVersion` | `LambdaLayer` |
| `AWS::Lambda::Permission` | `LambdaPermission` |
| `AWS::Lambda::EventSourceMapping` | `EventSourceMapping` |
| `AWS::ElasticLoadBalancingV2::LoadBalancer` | `ALB` |
| `AWS::ElasticLoadBalancingV2::TargetGroup` | `TargetGroup` |
| `AWS::ElasticLoadBalancingV2::Listener` | `Listener` |
| `AWS::ElasticLoadBalancingV2::ListenerRule` | `ListenerRule` |
| `AWS::SNS::Topic` | `SNSTopic` |
| `AWS::SNS::Subscription` | `SNSSubscription` |
| `AWS::SQS::Queue` | `SQSQueue` |
| `AWS::SQS::QueuePolicy` | `SQSQueuePolicy` |
| `AWS::ECR::Repository` | `ECRRepository` |
| `AWS::CertificateManager::Certificate` | `ACMCertificate` |
| `AWS::Logs::LogGroup` | `LogGroup` |
| `AWS::CloudWatch::Alarm` | `MetricAlarm` |
| `AWS::CloudWatch::Dashboard` | `Dashboard` |

### Crossplane (AWS provider) → Praxis

| Crossplane Kind | Praxis Kind |
|-----------------|-------------|
| `Bucket` | `S3Bucket` |
| `SecurityGroup` | `SecurityGroup` |
| `VPC` | `VPC` |
| `Subnet` | `Subnet` |
| `InternetGateway` | `InternetGateway` |
| `NATGateway` | `NATGateway` |
| `RouteTable` | `RouteTable` |
| `Instance` | `EC2Instance` |
| `Role` | `IAMRole` |
| `Policy` | `IAMPolicy` |

Anything not in these tables has **no Praxis equivalent** — report it to the user rather
than guessing (e.g. NLB exists as a Praxis kind but has no entry here for `aws_lb` with
`load_balancer_type = "network"`; inspect the source attributes before mapping).

## Reference: Praxis template shape

```cue
variables: {
    env:    string & ("dev" | "staging" | "prod")
    region: string | *"us-east-1"
}

resources: {
    myVpc: {
        apiVersion: "praxis.io/v1"
        kind:       "VPC"
        metadata: name: "main-\(variables.env)"
        spec: {
            region:    variables.region
            cidrBlock: "10.0.0.0/16"
        }
    }
    webSg: {
        apiVersion: "praxis.io/v1"
        kind:       "SecurityGroup"
        metadata: name: "web-\(variables.env)"
        spec: {
            region: variables.region
            vpcId:  "${resources.myVpc.outputs.vpcId}"
        }
    }
}
```

## See Also

- [create-template/SKILL.md](../create-template/SKILL.md) — authoring templates from scratch
- `docs/TEMPLATES.md` — full template reference
- `schemas/aws/` — per-kind spec schemas
