# Driver Roadmap

> **See also:** [Drivers](DRIVERS.md) | [Architecture](ARCHITECTURE.md)

This document tracks AWS driver coverage across Praxis releases. Driver priority is informed by Terraform AWS provider adoption, Crossplane provider-upjet-aws coverage, and industry cloud usage data. The full roadmap targets approximately 90% coverage of real-world AWS infrastructure patterns.

---

## Currently Released

39 drivers across 5 driver packs, covering core networking, compute, storage, database, DNS, identity management, load balancing, TLS certificates, and observability.

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
| | Lambda Function | Lambda functions | Region (`region~functionName`) |
| | Lambda Layer | Lambda layers | Region (`region~layerName`) |
| | Lambda Permission | Lambda resource-based policy statements | Custom (`region~functionName~statementId`) |
| | Event Source Mapping | Lambda event source mappings | Custom (`region~functionName~encodedArn`) |
| **Storage** | S3 Bucket | S3 buckets | Global (`bucketName`) |
| | EBS Volume | EBS volumes | Region (`region~name`) |
| | RDS Instance | RDS DB instances | Region (`region~dbIdentifier`) |
| | DB Subnet Group | DB subnet groups | Region (`region~groupName`) |
| | DB Parameter Group | DB parameter groups | Region (`region~groupName`) |
| | Aurora Cluster | Aurora DB clusters | Region (`region~clusterIdentifier`) |
| **Monitoring** | Log Group | CloudWatch log groups | Region (`region~logGroupName`) |
| | Metric Alarm | CloudWatch metric alarms | Region (`region~alarmName`) |
| | Dashboard | CloudWatch dashboards | Region (`region~dashboardName`) |
| **Identity** | IAM Role | IAM roles | Global (`roleName`) |
| | IAM Policy | IAM policies | Global (`policyName`) |
| | IAM User | IAM users | Global (`userName`) |
| | IAM Group | IAM groups | Global (`groupName`) |
| | IAM Instance Profile | Instance profiles | Global (`profileName`) |

---

## 1.0

Completes the services required for a standard web-application stack on AWS: container registry and messaging.

| Driver | Description | Driver Pack |
|---|---|---|
| **ECR** | Repositories, lifecycle policies | `praxis-compute` |
| **SNS** | Topics, subscriptions | `praxis-storage` |
| **SQS** | Queues, queue policies | `praxis-storage` |

---

## Future

Planned drivers ordered by real-world usage frequency. Together with the current and 1.0 drivers, these reach approximately 90% of infrastructure patterns users deploy in production.

### Tier 1 — Containers, Compute & Data

Core services present in most production AWS accounts.

| # | Driver | Resource Types | Key Scope |
|---|---|---|---|
| 1 | **ECS** | Clusters, services, task definitions, capacity providers | Region (`region~clusterName`) |
| 2 | **DynamoDB** | Tables, global tables, global secondary indexes | Region (`region~tableName`) |
| 3 | **KMS** | Keys, aliases, grants, key policies | Region (`region~keyAlias`) |
| 4 | **Auto Scaling** | Groups, policies, scheduled actions, launch configurations | Region (`region~asgName`) |
| 5 | **Launch Template** | EC2 launch templates, versions | Region (`region~templateName`) |
| 6 | **Secrets Manager** | Secrets, versions, rotation configuration | Region (`region~secretName`) |
| 7 | **CloudFront** | Distributions, OAC, cache policies, functions | Global (`distributionId`) |
| 8 | **EKS** | Clusters, managed node groups, Fargate profiles, add-ons | Region (`region~clusterName`) |
| 9 | **API Gateway** | REST APIs, HTTP APIs, stages, routes, authorizers | Region (`region~apiId`) |

### Tier 2 — Events, Workflows & Security

Services that complete event-driven, workflow, and security patterns.

| # | Driver | Resource Types | Key Scope |
|---|---|---|---|
| 10 | **Step Functions** | State machines, activities | Region (`region~stateMachineName`) |
| 11 | **EventBridge** | Event buses, rules, targets, pipes, schedules | Region (`region~busName`) |
| 12 | **WAFv2** | Web ACLs, rule groups, IP sets, regex pattern sets | Region/Global (`region~aclName`) |
| 13 | **Cognito** | User pools, identity pools, user pool clients, domains | Region (`region~poolName`) |
| 14 | **EFS** | File systems, mount targets, access points | Region (`region~fsName`) |
| 15 | **ElastiCache** | Clusters, replication groups, parameter groups, subnet groups | Region (`region~clusterName`) |
| 16 | **SSM Parameter** | Parameters, documents | Region (`region~paramName`) |

### Tier 3 — Enterprise Networking, CI/CD & Governance

Multi-account networking, deployment pipelines, and organizational governance.

| # | Driver | Resource Types | Key Scope |
|---|---|---|---|
| 17 | **Transit Gateway** | Transit gateways, attachments, route tables, peering | Region (`region~tgwName`) |
| 18 | **VPC Endpoints** | Interface/Gateway endpoints, endpoint services (PrivateLink) | Region (`region~endpointId`) |
| 19 | **CodeBuild** | Projects, webhooks, source credentials | Region (`region~projectName`) |
| 20 | **CodePipeline** | Pipelines, webhooks, custom action types | Region (`region~pipelineName`) |
| 21 | **CloudTrail** | Trails, event data stores | Region (`region~trailName`) |
| 22 | **Organizations** | Accounts, OUs, policies, SCPs, delegated admins | Global (`orgId`) |
| 23 | **Kinesis** | Data streams, Firehose delivery streams | Region (`region~streamName`) |
| 24 | **Glue** | Catalog databases, tables, crawlers, jobs, connections | Region (`region~resourceName`) |

### Tier 4 — Specialized Workloads

Data processing, compliance, managed services, and communication.

| # | Driver | Resource Types | Key Scope |
|---|---|---|---|
| 25 | **Batch** | Compute environments, job queues, job definitions | Region (`region~resourceName`) |
| 26 | **Config** | Config rules, recorders, aggregators, conformance packs | Region (`region~ruleName`) |
| 27 | **Network Firewall** | Firewalls, firewall policies, rule groups | Region (`region~firewallName`) |
| 28 | **Backup** | Plans, vaults, selections, vault policies | Region (`region~planName`) |
| 29 | **MSK** | Clusters, configurations, serverless clusters | Region (`region~clusterName`) |
| 30 | **Redshift** | Clusters, parameter groups, subnet groups | Region (`region~clusterName`) |
| 31 | **SES** | Domain identities, email identities, receipt rules, templates | Region (`region~identityName`) |
| 32 | **AppSync** | GraphQL APIs, data sources, resolvers, functions | Region (`region~apiName`) |
