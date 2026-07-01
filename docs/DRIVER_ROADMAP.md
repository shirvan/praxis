# Driver Roadmap

This document tracks current and available AWS driver coverage. Driver priority is ranked from measured ecosystem data — Terraform Registry module downloads and per-service AWS SDK package downloads — as described under [Future](#future).

---

## Currently Available

51 drivers across 5 driver packs, covering core networking, compute, storage, database, DNS, identity management, key management, secrets management, load balancing, TLS certificates, observability, container registry management, container orchestration, NoSQL databases, messaging, and configuration management.

| Pack | Driver | Resource Types | Key Scope |
|---|---|---|---|
| **Network** | VPC | Virtual Private Clouds | Region (`region~name`) |
| | Security Group | EC2 security groups | Custom (`vpcId~groupName`) |
| | Subnet | VPC subnets | Custom (`vpcId~name`) |
| | Route Table | Route tables, routes, subnet associations | Custom (`vpcId~name`) |
| | Internet Gateway | Internet gateways | Region (`region~name`) |
| | NAT Gateway | NAT gateways | Region (`region~name`) |
| | Network ACL | Network ACLs | Custom (`vpcId~name`) |
| | Elastic IP | Elastic IP addresses | Region (`region~name`) |
| | VPC Peering | VPC peering connections | Region (`region~name`) |
| | Hosted Zone | Route 53 hosted zones | Global (`hostedZoneId`) |
| | DNS Record | Route 53 records | Global (`hostedZoneId~recordName`) |
| | Health Check | Route 53 health checks | Global (`healthCheckId`) |
| | ALB | Application Load Balancers | Region (`region~albName`) |
| | NLB | Network Load Balancers | Region (`region~nlbName`) |
| | Target Group | ELB target groups | Region (`region~tgName`) |
| | Listener | ELB listeners | Region (`region~listenerName`) |
| | Listener Rule | ELB listener rules | Region (`region~ruleName`) |
| | ACM Certificate | ACM certificates, DNS validation records | Region (`region~domainName`) |
| **Compute** | EC2 Instance | EC2 instances | Region (`region~name`) |
| | AMI | Amazon Machine Images | Region (`region~amiName`) |
| | KeyPair | EC2 key pairs | Region (`region~keyName`) |
| | ECR Repository | ECR repositories | Region (`region~repositoryName`) |
| | ECR Lifecycle Policy | Repository lifecycle policies | Custom (`region~repositoryName`) |
| | Lambda Function | Lambda functions | Region (`region~functionName`) |
| | Lambda Layer | Lambda layers | Region (`region~layerName`) |
| | Lambda Permission | Lambda resource-based policy statements | Custom (`region~functionName~statementId`) |
| | Event Source Mapping | Lambda event source mappings | Custom (`region~functionName~encodedArn`) |
| | EKS Cluster | EKS control-plane clusters | Region (`region~clusterName`) |
| | ECS Cluster | ECS clusters, settings, capacity providers | Region (`region~clusterName`) |
| **Storage** | S3 Bucket | S3 buckets | Global (`bucketName`) |
| | EBS Volume | EBS volumes | Region (`region~name`) |
| | RDS Instance | RDS DB instances | Region (`region~dbIdentifier`) |
| | DB Subnet Group | DB subnet groups | Region (`region~groupName`) |
| | DB Parameter Group | DB parameter groups | Region (`region~groupName`) |
| | Aurora Cluster | Aurora DB clusters | Region (`region~clusterIdentifier`) |
| | SNS Topic | SNS topics | Region (`region~topicName`) |
| | SNS Subscription | SNS subscriptions | Custom (`region~subscriptionName`) |
| | SQS Queue | SQS queues | Region (`region~queueName`) |
| | SQS Queue Policy | SQS queue resource policies | Region (`region~queueName`) |
| | SSM Parameter | SSM Parameter Store parameters | Region (`region~paramName`) |
| | DynamoDB Table | DynamoDB tables | Region (`region~tableName`) |
| **Monitoring** | Log Group | CloudWatch log groups | Region (`region~logGroupName`) |
| | Metric Alarm | CloudWatch metric alarms | Region (`region~alarmName`) |
| | Dashboard | CloudWatch dashboards | Region (`region~dashboardName`) |
| **Identity** | KMS Key | KMS keys, aliases, rotation | Region (`region~keyAlias`) |
| | Secrets Manager Secret | Secrets Manager secrets and versions | Region (`region~secretName`) |
| | IAM Role | IAM roles | Global (`roleName`) |
| | IAM Policy | IAM policies | Global (`policyName`) |
| | IAM User | IAM users | Global (`userName`) |
| | IAM Group | IAM groups | Global (`groupName`) |
| | IAM Instance Profile | Instance profiles | Global (`profileName`) |

---

## Future

Drivers not yet implemented, ordered by measured real-world usage. Together with the currently available drivers, these cover approximately 90% of infrastructure patterns users deploy in production.

Ranking is based on two independent signals, collected June 2026:

- **Terraform Registry** all-time download counts for the `terraform-aws-modules/*` modules — the primary signal, since it measures what teams actually provision through IaC.
- **npm** last-month download counts for the per-service `@aws-sdk/client-*` packages — a secondary signal that fills gaps where no popular Terraform module exists (e.g. Cognito, Kinesis, SES). SDK downloads measure runtime API usage, so they under-count provision-only services (Auto Scaling, WAF, EKS) and over-count runtime-heavy ones (SES, SSM parameter reads); placements weigh both signals accordingly.

### Tier 1 — Core Platform Services

Services present in most production AWS accounts. The core resource for each of EKS, KMS, Secrets Manager, DynamoDB, and ECS has now shipped (see [Currently Available](#currently-available)); the entries below track the remaining sub-resources for each.

| # | Driver | Resource Types | Key Scope |
|---|---|---|---|
| 1 | **EKS** | Managed node groups, Fargate profiles, add-ons (control-plane cluster shipped) | Region (`region~clusterName`) |
| 2 | **KMS** | Grants, key policies (keys, aliases, rotation shipped) | Region (`region~keyAlias`) |
| 3 | **Secrets Manager** | Rotation configuration (secrets, versions shipped) | Region (`region~secretName`) |
| 4 | **DynamoDB** | Global tables, global secondary indexes (base tables shipped) | Region (`region~tableName`) |
| 5 | **ECS** | Services, task definitions, capacity provider associations (clusters shipped) | Region (`region~clusterName`) |
| 6 | **CloudFront** | Distributions, OAC, cache policies, functions | Global (`distributionId`) |
| 7 | **EventBridge** | Event buses, rules, targets, pipes, schedules | Region (`region~busName`) |
| 8 | **Auto Scaling** | Groups, policies, scheduled actions, launch configurations | Region (`region~asgName`) |
| 9 | **Launch Template** | EC2 launch templates, versions | Region (`region~templateName`) |
| 10 | **API Gateway** | REST APIs, HTTP APIs, stages, routes, authorizers | Region (`region~apiId`) |

### Tier 2 — Workflows, Data & Security

Services that complete identity, streaming, workflow, and security patterns. Kinesis + Firehose (19.7M npm combined), Cognito (12.3M npm), and SES (22.5M npm across both clients) all measure substantially higher than their previous placement suggested.

| # | Driver | Resource Types | Key Scope |
|---|---|---|---|
| 11 | **Cognito** | User pools, identity pools, user pool clients, domains | Region (`region~poolName`) |
| 12 | **Kinesis** | Data streams, Firehose delivery streams | Region (`region~streamName`) |
| 13 | **Step Functions** | State machines, activities | Region (`region~stateMachineName`) |
| 14 | **SES** | Domain identities, email identities, receipt rules, templates | Region (`region~identityName`) |
| 15 | **ElastiCache** | Clusters, replication groups, parameter groups, subnet groups | Region (`region~clusterName`) |
| 16 | **EFS** | File systems, mount targets, access points | Region (`region~fsName`) |
| 17 | **WAFv2** | Web ACLs, rule groups, IP sets, regex pattern sets | Region/Global (`region~aclName`) |
| 18 | **Bedrock** | Guardrails, knowledge bases, provisioned model throughput | Region (`region~resourceName`) |

### Tier 3 — Enterprise Networking, CI/CD & Governance

Multi-account networking, deployment pipelines, analytics, and organizational governance.

| # | Driver | Resource Types | Key Scope |
|---|---|---|---|
| 19 | **Transit Gateway** | Transit gateways, attachments, route tables, peering | Region (`region~tgwName`) |
| 20 | **VPC Endpoints** | Interface/Gateway endpoints, endpoint services (PrivateLink) | Region (`region~endpointId`) |
| 21 | **Athena** | Workgroups, data catalogs | Region (`region~workgroupName`) |
| 22 | **CodeBuild** | Projects, webhooks, source credentials | Region (`region~projectName`) |
| 23 | **CodePipeline** | Pipelines, webhooks, custom action types | Region (`region~pipelineName`) |
| 24 | **CloudTrail** | Trails, event data stores | Region (`region~trailName`) |
| 25 | **Organizations** | Accounts, OUs, policies, SCPs, delegated admins | Global (`orgId`) |
| 26 | **Glue** | Catalog databases, tables, crawlers, jobs, connections | Region (`region~resourceName`) |

### Tier 4 — Specialized Workloads

Data processing, compliance, managed services, and communication. SSM documents were split out of the shipped SSM Parameter driver: the SDK download signal for SSM is dominated by parameter reads, so document management ranks here on its own merits.

| # | Driver | Resource Types | Key Scope |
|---|---|---|---|
| 27 | **Batch** | Compute environments, job queues, job definitions | Region (`region~resourceName`) |
| 28 | **Redshift** | Clusters, parameter groups, subnet groups | Region (`region~clusterName`) |
| 29 | **OpenSearch** | Domains, access policies | Region (`region~domainName`) |
| 30 | **MSK** | Clusters, configurations, serverless clusters | Region (`region~clusterName`) |
| 31 | **AppSync** | GraphQL APIs, data sources, resolvers, functions | Region (`region~apiName`) |
| 32 | **Backup** | Plans, vaults, selections, vault policies | Region (`region~planName`) |
| 33 | **Config** | Config rules, recorders, aggregators, conformance packs | Region (`region~ruleName`) |
| 34 | **Network Firewall** | Firewalls, firewall policies, rule groups | Region (`region~firewallName`) |
| 35 | **SSM Document** | SSM documents | Region (`region~documentName`) |

### Evaluated and excluded

Services measured but intentionally left off the roadmap due to low adoption signals (all under ~1M npm monthly / ~2M Terraform downloads): DocumentDB, Neptune, MemoryDB, App Runner, Amazon MQ, Transfer Family, X-Ray, Cloud Map, Amplify.
