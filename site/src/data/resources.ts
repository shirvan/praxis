export type ResourceScope = 'Regional' | 'Global' | 'Composite';

export interface ResourceDetails {
  lookupFilters: string[];
  outputs: string[];
  importIdentifier: string;
  example: string;
}

export interface PraxisResource {
  kind: string;
  slug: string;
  service: string;
  domain: string;
  description: string;
  schema: string;
  scope: ResourceScope;
  lookup: boolean;
  details?: ResourceDetails;
}

export const resources: PraxisResource[] = [
  { kind: 'ACMCertificate', slug: 'acm-certificate', service: 'ACM', domain: 'Operations', description: 'Provision and reconcile public or private TLS certificates.', schema: 'schemas/aws/acm/certificate.cue', scope: 'Regional', lookup: true },
  { kind: 'Dashboard', slug: 'cloudwatch-dashboard', service: 'CloudWatch', domain: 'Operations', description: 'Manage CloudWatch dashboard bodies as declared infrastructure.', schema: 'schemas/aws/cloudwatch/dashboard.cue', scope: 'Global', lookup: true },
  {
    kind: 'LogGroup', slug: 'cloudwatch-log-group', service: 'CloudWatch', domain: 'Operations', description: 'Manage log groups, retention, encryption, and tags.', schema: 'schemas/aws/cloudwatch/log_group.cue', scope: 'Regional', lookup: true,
    details: { lookupFilters: ['id', 'name', 'id/name + tag'], outputs: ['arn', 'logGroupName', 'logGroupClass', 'retentionInDays', 'kmsKeyId', 'creationTime', 'storedBytes'], importIdentifier: 'Log group name and region', example: `resources: logs: {
  apiVersion: "praxis.io/alpha"
  kind: "LogGroup"
  metadata: {name: "/praxis/payments", labels: {}}
  spec: {
    region: "us-west-2"
    retentionInDays: 30
    tags: environment: "prod"
  }
}` }
  },
  { kind: 'MetricAlarm', slug: 'cloudwatch-metric-alarm', service: 'CloudWatch', domain: 'Operations', description: 'Declare metric alarms and their evaluation behavior.', schema: 'schemas/aws/cloudwatch/metric_alarm.cue', scope: 'Regional', lookup: true },
  {
    kind: 'DynamoDBTable', slug: 'dynamodb-table', service: 'DynamoDB', domain: 'Data', description: 'Manage DynamoDB tables, keys, billing, indexes, and protection.', schema: 'schemas/aws/dynamodb/table.cue', scope: 'Regional', lookup: true,
    details: { lookupFilters: ['id', 'name', 'id/name + tag'], outputs: ['arn', 'name', 'status', 'itemCount'], importIdentifier: 'Table name and region', example: `resources: sessions: {
  apiVersion: "praxis.io/alpha"
  kind: "DynamoDBTable"
  metadata: {name: "payments-sessions", labels: {}}
  spec: {
    region: "us-west-2"
    billingMode: "PAY_PER_REQUEST"
    hashKey: "sessionId"
    hashKeyType: "S"
    tags: environment: "prod"
  }
}` }
  },
  { kind: 'EBSVolume', slug: 'ebs-volume', service: 'EC2', domain: 'Compute', description: 'Provision and reconcile EBS block volumes.', schema: 'schemas/aws/ebs/ebs.cue', scope: 'Regional', lookup: true },
  { kind: 'AMI', slug: 'ami', service: 'EC2', domain: 'Compute', description: 'Create and track Amazon Machine Images.', schema: 'schemas/aws/ec2/ami.cue', scope: 'Regional', lookup: true },
  {
    kind: 'EC2Instance', slug: 'ec2-instance', service: 'EC2', domain: 'Compute', description: 'Manage EC2 instances and their declared runtime configuration.', schema: 'schemas/aws/ec2/ec2.cue', scope: 'Regional', lookup: true,
    details: { lookupFilters: ['id', 'name', 'tag'], outputs: ['instanceId', 'privateIpAddress', 'publicIpAddress', 'privateDnsName', 'state', 'subnetId', 'vpcId'], importIdentifier: 'EC2 instance ID and region', example: `resources: app: {
  apiVersion: "praxis.io/alpha"
  kind: "EC2Instance"
  metadata: {name: "payments-api", labels: {}}
  spec: {
    region: "us-west-2"
    imageId: "ami-0123456789abcdef0"
    instanceType: "t3.small"
    subnetId: "\${resources.appSubnet.outputs.subnetId}"
    securityGroupIds: ["\${resources.appSecurity.outputs.groupId}"]
    tags: environment: "prod"
  }
}` }
  },
  { kind: 'ElasticIP', slug: 'elastic-ip', service: 'EC2', domain: 'Networking', description: 'Allocate and associate Elastic IP addresses.', schema: 'schemas/aws/ec2/eip.cue', scope: 'Regional', lookup: true },
  { kind: 'KeyPair', slug: 'key-pair', service: 'EC2', domain: 'Identity', description: 'Manage EC2 key pairs for instance access.', schema: 'schemas/aws/ec2/keypair.cue', scope: 'Regional', lookup: true },
  {
    kind: 'SecurityGroup', slug: 'security-group', service: 'EC2', domain: 'Networking', description: 'Manage VPC security groups and ingress or egress rules.', schema: 'schemas/aws/ec2/sg.cue', scope: 'Composite', lookup: true,
    details: { lookupFilters: ['id', 'name', 'tag'], outputs: ['groupId', 'groupArn', 'vpcId'], importIdentifier: 'Security group ID, for example sg-0123456789abcdef0', example: `resources: web: {
  apiVersion: "praxis.io/alpha"
  kind: "SecurityGroup"
  metadata: {name: "web", labels: {}}
  spec: {
    groupName: "web"
    description: "HTTPS ingress"
    vpcId: "\${resources.network.outputs.vpcId}"
    ingressRules: [{
      protocol: "tcp"
      fromPort: 443
      toPort: 443
      cidrBlock: "0.0.0.0/0"
    }]
    tags: environment: "prod"
  }
}` }
  },
  { kind: 'ECRLifecyclePolicy', slug: 'ecr-lifecycle-policy', service: 'ECR', domain: 'Data', description: 'Attach declarative image retention policies to ECR repositories.', schema: 'schemas/aws/ecr/lifecycle_policy.cue', scope: 'Regional', lookup: true },
  {
    kind: 'ECRRepository', slug: 'ecr-repository', service: 'ECR', domain: 'Data', description: 'Manage container registries, encryption, scanning, and tags.', schema: 'schemas/aws/ecr/repository.cue', scope: 'Regional', lookup: true,
    details: { lookupFilters: ['id', 'name', 'id/name + tag'], outputs: ['repositoryArn', 'repositoryName', 'repositoryUri', 'registryId'], importIdentifier: 'Repository name and region', example: `resources: images: {
  apiVersion: "praxis.io/alpha"
  kind: "ECRRepository"
  metadata: {name: "payments", labels: {}}
  spec: {
    region: "us-west-2"
    imageTagMutability: "IMMUTABLE"
    imageScanningConfiguration: scanOnPush: true
    tags: environment: "prod"
  }
}` }
  },
  {
    kind: 'ECSCluster', slug: 'ecs-cluster', service: 'ECS', domain: 'Compute', description: 'Create and reconcile ECS clusters and settings.', schema: 'schemas/aws/ecs/cluster.cue', scope: 'Regional', lookup: true,
    details: { lookupFilters: ['id', 'name', 'id/name + tag'], outputs: ['arn', 'name', 'status'], importIdentifier: 'Cluster name or ARN and region', example: `resources: compute: {
  apiVersion: "praxis.io/alpha"
  kind: "ECSCluster"
  metadata: {name: "payments", labels: {}}
  spec: {
    region: "us-west-2"
    containerInsights: "enabled"
    capacityProviders: ["FARGATE", "FARGATE_SPOT"]
    tags: environment: "prod"
  }
}` }
  },
  { kind: 'EKSCluster', slug: 'eks-cluster', service: 'EKS', domain: 'Compute', description: 'Manage the lifecycle and configuration of EKS clusters.', schema: 'schemas/aws/eks/cluster.cue', scope: 'Regional', lookup: true },
  { kind: 'ALB', slug: 'application-load-balancer', service: 'Elastic Load Balancing', domain: 'Networking', description: 'Manage Application Load Balancers and network placement.', schema: 'schemas/aws/elb/alb.cue', scope: 'Regional', lookup: true },
  { kind: 'Listener', slug: 'load-balancer-listener', service: 'Elastic Load Balancing', domain: 'Networking', description: 'Declare load balancer listeners and default actions.', schema: 'schemas/aws/elb/listener.cue', scope: 'Composite', lookup: true },
  { kind: 'ListenerRule', slug: 'listener-rule', service: 'Elastic Load Balancing', domain: 'Networking', description: 'Manage listener routing rules, conditions, and priority.', schema: 'schemas/aws/elb/listener_rule.cue', scope: 'Composite', lookup: true },
  { kind: 'NLB', slug: 'network-load-balancer', service: 'Elastic Load Balancing', domain: 'Networking', description: 'Manage Network Load Balancers and network placement.', schema: 'schemas/aws/elb/nlb.cue', scope: 'Regional', lookup: true },
  { kind: 'TargetGroup', slug: 'target-group', service: 'Elastic Load Balancing', domain: 'Networking', description: 'Declare target groups, protocols, ports, and health checks.', schema: 'schemas/aws/elb/target_group.cue', scope: 'Regional', lookup: true },
  { kind: 'IAMGroup', slug: 'iam-group', service: 'IAM', domain: 'Identity', description: 'Manage IAM groups and policy attachments.', schema: 'schemas/aws/iam/group.cue', scope: 'Global', lookup: true },
  { kind: 'IAMInstanceProfile', slug: 'iam-instance-profile', service: 'IAM', domain: 'Identity', description: 'Manage instance profiles and their associated role.', schema: 'schemas/aws/iam/instance_profile.cue', scope: 'Global', lookup: true },
  { kind: 'IAMPolicy', slug: 'iam-policy', service: 'IAM', domain: 'Identity', description: 'Manage customer-managed IAM policies and policy documents.', schema: 'schemas/aws/iam/policy.cue', scope: 'Global', lookup: true },
  {
    kind: 'IAMRole', slug: 'iam-role', service: 'IAM', domain: 'Identity', description: 'Manage IAM roles, trust policies, permissions, and tags.', schema: 'schemas/aws/iam/role.cue', scope: 'Global', lookup: true,
    details: { lookupFilters: ['id', 'name', 'tag'], outputs: ['arn', 'roleId', 'roleName'], importIdentifier: 'Role name or role ARN', example: `resources: appRole: {
  apiVersion: "praxis.io/alpha"
  kind: "IAMRole"
  metadata: {name: "payments-api", labels: {}}
  spec: {
    path: "/services/"
    assumeRolePolicyDocument: json.Marshal({
      Version: "2012-10-17"
      Statement: [{
        Effect: "Allow"
        Principal: Service: "ec2.amazonaws.com"
        Action: "sts:AssumeRole"
      }]
    })
    inlinePolicies: {}
    tags: environment: "prod"
  }
}` }
  },
  { kind: 'IAMUser', slug: 'iam-user', service: 'IAM', domain: 'Identity', description: 'Manage IAM users, tags, groups, and policy attachments.', schema: 'schemas/aws/iam/user.cue', scope: 'Global', lookup: true },
  { kind: 'InternetGateway', slug: 'internet-gateway', service: 'VPC', domain: 'Networking', description: 'Create internet gateways and attach them to VPCs.', schema: 'schemas/aws/igw/igw.cue', scope: 'Regional', lookup: true },
  { kind: 'KMSKey', slug: 'kms-key', service: 'KMS', domain: 'Identity', description: 'Manage KMS keys, policies, rotation, and lifecycle controls.', schema: 'schemas/aws/kms/key.cue', scope: 'Regional', lookup: true },
  { kind: 'EventSourceMapping', slug: 'lambda-event-source-mapping', service: 'Lambda', domain: 'Compute', description: 'Connect Lambda functions to streams and queues.', schema: 'schemas/aws/lambda/event_source_mapping.cue', scope: 'Composite', lookup: true },
  {
    kind: 'LambdaFunction', slug: 'lambda-function', service: 'Lambda', domain: 'Compute', description: 'Manage Lambda code, runtime configuration, roles, and networking.', schema: 'schemas/aws/lambda/function.cue', scope: 'Regional', lookup: true,
    details: { lookupFilters: ['id', 'name', 'id/name + tag'], outputs: ['functionArn', 'functionName', 'version', 'state', 'lastModified', 'lastUpdateStatus', 'codeSha256'], importIdentifier: 'Function name and region', example: `resources: processor: {
  apiVersion: "praxis.io/alpha"
  kind: "LambdaFunction"
  metadata: {name: "payments-processor", labels: {}}
  spec: {
    region: "us-west-2"
    role: "\${resources.functionRole.outputs.arn}"
    runtime: "provided.al2023"
    handler: "bootstrap"
    code: s3: {
      bucket: "release-artifacts"
      key: "payments/processor.zip"
    }
    tags: environment: "prod"
  }
}` }
  },
  { kind: 'LambdaLayer', slug: 'lambda-layer', service: 'Lambda', domain: 'Compute', description: 'Publish and track reusable Lambda layers.', schema: 'schemas/aws/lambda/layer.cue', scope: 'Regional', lookup: true },
  { kind: 'LambdaPermission', slug: 'lambda-permission', service: 'Lambda', domain: 'Identity', description: 'Manage resource-based invocation permissions for functions.', schema: 'schemas/aws/lambda/permission.cue', scope: 'Composite', lookup: true },
  { kind: 'NetworkACL', slug: 'network-acl', service: 'VPC', domain: 'Networking', description: 'Manage VPC network ACLs, associations, and entries.', schema: 'schemas/aws/nacl/nacl.cue', scope: 'Regional', lookup: true },
  { kind: 'NATGateway', slug: 'nat-gateway', service: 'VPC', domain: 'Networking', description: 'Provision NAT gateways in declared subnets.', schema: 'schemas/aws/natgw/natgw.cue', scope: 'Regional', lookup: true },
  { kind: 'AuroraCluster', slug: 'aurora-cluster', service: 'RDS', domain: 'Data', description: 'Manage Aurora database clusters and core configuration.', schema: 'schemas/aws/rds/aurora_cluster.cue', scope: 'Regional', lookup: true },
  { kind: 'DBParameterGroup', slug: 'db-parameter-group', service: 'RDS', domain: 'Data', description: 'Manage database parameter groups and declared parameters.', schema: 'schemas/aws/rds/db_parameter_group.cue', scope: 'Regional', lookup: true },
  { kind: 'DBSubnetGroup', slug: 'db-subnet-group', service: 'RDS', domain: 'Data', description: 'Manage database subnet groups across availability zones.', schema: 'schemas/aws/rds/db_subnet_group.cue', scope: 'Regional', lookup: true },
  {
    kind: 'RDSInstance', slug: 'rds-instance', service: 'RDS', domain: 'Data', description: 'Manage relational database instances and lifecycle settings.', schema: 'schemas/aws/rds/instance.cue', scope: 'Regional', lookup: true,
    details: { lookupFilters: ['id', 'name', 'id/name + tag'], outputs: ['dbIdentifier', 'dbiResourceId', 'arn', 'endpoint', 'port', 'engine', 'engineVersion', 'status'], importIdentifier: 'DB instance identifier and region', example: `resources: database: {
  apiVersion: "praxis.io/alpha"
  kind: "RDSInstance"
  metadata: {name: "payments-primary", labels: {}}
  spec: {
    region: "us-west-2"
    engine: "postgres"
    engineVersion: "16.3"
    instanceClass: "db.t3.small"
    allocatedStorage: 20
    masterUsername: "payments"
    masterUserPassword: "ssm:///praxis/payments/db-password"
    tags: environment: "prod"
  }
  lifecycle: preventDestroy: true
}` }
  },
  { kind: 'Route53HealthCheck', slug: 'route53-health-check', service: 'Route 53', domain: 'Operations', description: 'Manage Route 53 endpoint and calculated health checks.', schema: 'schemas/aws/route53/health_check.cue', scope: 'Global', lookup: true },
  {
    kind: 'Route53HostedZone', slug: 'route53-hosted-zone', service: 'Route 53', domain: 'Operations', description: 'Manage public and private DNS hosted zones.', schema: 'schemas/aws/route53/hosted_zone.cue', scope: 'Global', lookup: true,
    details: { lookupFilters: ['id', 'name', 'tag'], outputs: ['hostedZoneId', 'name', 'nameServers', 'isPrivate', 'recordCount'], importIdentifier: 'Hosted zone ID, for example Z0123456789ABCDEF', example: `resources: zone: {
  apiVersion: "praxis.io/alpha"
  kind: "Route53HostedZone"
  metadata: {name: "example.com", labels: {}}
  spec: {
    comment: "Production DNS"
    tags: environment: "prod"
  }
}` }
  },
  { kind: 'Route53Record', slug: 'route53-record', service: 'Route 53', domain: 'Operations', description: 'Manage DNS record sets inside a hosted zone.', schema: 'schemas/aws/route53/record.cue', scope: 'Composite', lookup: true },
  { kind: 'RouteTable', slug: 'route-table', service: 'VPC', domain: 'Networking', description: 'Manage routes and subnet associations for a VPC.', schema: 'schemas/aws/routetable/routetable.cue', scope: 'Regional', lookup: true },
  {
    kind: 'S3Bucket', slug: 's3-bucket', service: 'S3', domain: 'Data', description: 'Manage S3 buckets, encryption, versioning, ACLs, and tags.', schema: 'schemas/aws/s3/s3.cue', scope: 'Global', lookup: true,
    details: { lookupFilters: ['id', 'name', 'tag'], outputs: ['arn', 'bucketName', 'region', 'domainName'], importIdentifier: 'Bucket name', example: `resources: archive: {
  apiVersion: "praxis.io/alpha"
  kind: "S3Bucket"
  metadata: {name: "payments-prod-archive", labels: {}}
  spec: {
    region: "us-west-2"
    versioning: true
    encryption: {
      enabled: true
      algorithm: "AES256"
    }
    tags: environment: "prod"
  }
}` }
  },
  { kind: 'SecretsManagerSecret', slug: 'secrets-manager-secret', service: 'Secrets Manager', domain: 'Data', description: 'Manage secret metadata, encryption, recovery, and tags.', schema: 'schemas/aws/secretsmanager/secret.cue', scope: 'Regional', lookup: true },
  { kind: 'SNSSubscription', slug: 'sns-subscription', service: 'SNS', domain: 'Data', description: 'Connect SNS topics to HTTP, queue, email, or Lambda endpoints.', schema: 'schemas/aws/sns/subscription.cue', scope: 'Composite', lookup: true },
  { kind: 'SNSTopic', slug: 'sns-topic', service: 'SNS', domain: 'Data', description: 'Manage notification topics, encryption, policies, and tags.', schema: 'schemas/aws/sns/topic.cue', scope: 'Regional', lookup: true },
  { kind: 'SQSQueue', slug: 'sqs-queue', service: 'SQS', domain: 'Data', description: 'Manage queues, delivery behavior, encryption, and dead letters.', schema: 'schemas/aws/sqs/queue.cue', scope: 'Regional', lookup: true },
  { kind: 'SQSQueuePolicy', slug: 'sqs-queue-policy', service: 'SQS', domain: 'Identity', description: 'Attach declarative access policies to SQS queues.', schema: 'schemas/aws/sqs/queue_policy.cue', scope: 'Composite', lookup: true },
  { kind: 'SSMParameter', slug: 'ssm-parameter', service: 'Systems Manager', domain: 'Data', description: 'Manage Parameter Store values, types, tiers, and tags.', schema: 'schemas/aws/ssm/parameter.cue', scope: 'Regional', lookup: true },
  {
    kind: 'Subnet', slug: 'subnet', service: 'VPC', domain: 'Networking', description: 'Manage VPC subnets, availability zones, IP behavior, and tags.', schema: 'schemas/aws/subnet/subnet.cue', scope: 'Composite', lookup: true,
    details: { lookupFilters: ['id', 'name', 'tag'], outputs: ['subnetId', 'arn', 'vpcId', 'cidrBlock', 'availabilityZone', 'availabilityZoneId', 'mapPublicIpOnLaunch', 'state', 'ownerId', 'availableIpCount'], importIdentifier: 'Subnet ID and region, for example subnet-0123456789abcdef0 in us-west-2', example: `resources: app: {
  apiVersion: "praxis.io/alpha"
  kind: "Subnet"
  metadata: {name: "app-a", labels: {}}
  spec: {
    region: "us-west-2"
    vpcId: "\${resources.network.outputs.vpcId}"
    cidrBlock: "10.42.10.0/24"
    availabilityZone: "us-west-2a"
    mapPublicIpOnLaunch: false
    tags: tier: "application"
  }
}` }
  },
  {
    kind: 'VPC', slug: 'vpc', service: 'VPC', domain: 'Networking', description: 'Manage VPC address space, DNS settings, tenancy, and tags.', schema: 'schemas/aws/vpc/vpc.cue', scope: 'Regional', lookup: true,
    details: { lookupFilters: ['id', 'name', 'tag'], outputs: ['vpcId', 'arn', 'cidrBlock', 'state', 'enableDnsHostnames', 'enableDnsSupport', 'instanceTenancy', 'ownerId', 'dhcpOptionsId', 'isDefault'], importIdentifier: 'VPC ID and region, for example vpc-0123456789abcdef0 in us-west-2', example: `resources: network: {
  apiVersion: "praxis.io/alpha"
  kind: "VPC"
  metadata: {name: "payments", labels: {}}
  spec: {
    region: "us-west-2"
    cidrBlock: "10.42.0.0/16"
    enableDnsHostnames: true
    enableDnsSupport: true
    tags: environment: "prod"
  }
}` }
  },
  { kind: 'VPCPeeringConnection', slug: 'vpc-peering-connection', service: 'VPC', domain: 'Networking', description: 'Manage peering connections between VPCs.', schema: 'schemas/aws/vpcpeering/vpcpeering.cue', scope: 'Composite', lookup: true },
];

export const domains = ['All', 'Networking', 'Compute', 'Data', 'Identity', 'Operations'] as const;

export function resourceBySlug(slug: string): PraxisResource | undefined {
  return resources.find((resource) => resource.slug === slug);
}
