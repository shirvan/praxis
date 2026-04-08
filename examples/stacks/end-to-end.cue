import "encoding/json"

// end-to-end.cue — Full-coverage test template: every Praxis resource kind
// and feature exercised in a single, architecturally coherent stack.
//
// This template exists specifically for end-to-end lifecycle testing. It is
// NOT intended as a production reference — see saas-platform.cue for a
// realistic SaaS deployment example.
//
// ┌────────────────────────────────────────────────────────────────────────┐
// │                           ARCHITECTURE                                 │
// │                                                                        │
// │  Shared-Services VPC (data source — pre-existing)                      │
// │  └── VPC Peering ─────────────────────────────────────────┐            │
// │                                                           │            │
// │  Platform VPC (10.0.0.0/16) — Multi-AZ, DNS-enabled       │            │
// │  ├── Internet Gateway                                     │            │
// │  ├── Public Subnets (AZ-a, AZ-b)                          │            │
// │  │   ├── Route Table → IGW                                │            │
// │  │   ├── NAT Gateway + Elastic IP                         │            │
// │  │   ├── ALB (HTTPS) → Target Group → Listener Rules      │            │
// │  │   │   └── HTTP→HTTPS redirect listener                 │            │
// │  │   └── NLB (internal, TCP/gRPC) → gRPC Target Group     │            │
// │  ├── App Subnets (AZ-a, AZ-b)                             │            │
// │  │   ├── Route Table → NAT                                │            │
// │  │   ├── App Security Group (8080 from ALB SG)            │            │
// │  │   ├── SSH Key Pair                                     │            │
// │  │   ├── App Servers (per AZ, for-loop + Instance Profile)│            │
// │  │   ├── EBS Data Volumes (per AZ, for-loop)              │            │
// │  │   └── Lambda (worker + shared layer + SQS trigger)     │            │
// │  │       └── Lambda Permission (SNS→invoke)               │            │
// │  ├── Data Subnets (AZ-a, AZ-b)                            │            │
// │  │   ├── Route Table → NAT                                │            │
// │  │   ├── DB Subnet Group + Parameter Group                │            │
// │  │   ├── Data Security Group (5432 from App SG CIDR)      │            │
// │  │   ├── RDS PostgreSQL (Multi-AZ in prod)                │            │
// │  │   └── Aurora Cluster (conditional, when enableAurora)  │            │
// │  ├── Network ACL (defense-in-depth on public subnets)     │            │
// │  ├── ACM Certificate + DNS Validation Record              │            │
// │  ├── Route 53 Health Check → alarm integration            │            │
// │  ├── SNS Alert Topic + SQS Dead-Letter Queue              │            │
// │  │   ├── SNS → SQS Subscription + Queue Policy            │            │
// │  │   └── SQS → Lambda Event Source Mapping                │            │
// │  ├── CloudWatch Log Group + Metric Alarm + Dashboard      │            │
// │  ├── IAM Role + Policy + Instance Profile                 │            │
// │  ├── IAM Group (ops) + Service User (CI/CD)               │            │
// │  ├── ECR Repository + Lifecycle Policy                    │            │
// │  ├── Golden AMI (conditional, when enableGoldenAmi)       │            │
// │  └── S3 Buckets (dynamic from list + conditional logger)  │            │
// └────────────────────────────────────────────────────────────────────────┘
//
// Every Praxis feature exercised:
//   ✓ Variables — typed, constrained (regex, enum, defaults, lists, bools)
//   ✓ Data sources — live lookup of pre-existing shared-services VPC
//   ✓ For-loop comprehensions — app servers, EBS volumes, S3 buckets
//   ✓ Conditional resources — Aurora, golden AMI, log bucket, monitoring, alarm
//   ✓ CUE definitions (#) — reusable tag blocks (#StandardTags, #DataTags, …)
//   ✓ Hidden helpers (_) — _naming prefix
//   ✓ Output expressions — full DAG wiring across 45+ resources
//   ✓ Lifecycle rules — preventDestroy on database + ignoreChanges
//   ✓ Environment-aware logic — Multi-AZ RDS, monitoring, instance sizing
//   ✓ SSM secret references — database master password
//   ✓ All 45 resource kinds:
//       VPC, Subnet, IGW, NATGateway, EIP, RouteTable, SecurityGroup, NACL,
//       VPCPeering, EC2, KeyPair, EBS, AMI, IAMRole, IAMPolicy,
//       IAMInstanceProfile, IAMGroup, IAMUser, ACMCertificate,
//       Route53HostedZone, Route53Record, Route53HealthCheck,
//       ALB, NLB, TargetGroup, Listener, ListenerRule,
//       LambdaFunction, LambdaLayer, LambdaPermission, EventSourceMapping,
//       RDSInstance, AuroraCluster, DBSubnetGroup, DBParameterGroup,
//       S3Bucket, ECRRepository, ECRLifecyclePolicy,
//       SNSTopic, SNSSubscription, SQSQueue, SQSQueuePolicy,
//       LogGroup, MetricAlarm, Dashboard
//
// Usage:
//   praxis template register examples/stacks/end-to-end.cue \
//     --description "Full-coverage test stack — all 45 resource kinds"
//
//   praxis deploy end-to-end --account local \
//     -f examples/stacks/end-to-end.vars.json --dry-run
//
//   praxis deploy end-to-end --account local \
//     -f examples/stacks/end-to-end.vars.json --key e2e-dev --wait

// ═══════════════════════════════════════════════════════════════════════
// VARIABLES
// ═══════════════════════════════════════════════════════════════════════

variables: {
	name:         string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment:  "dev" | "staging" | "prod"
	instanceType: string | *"t3.small"
	imageId:      string | *"ami-0885b1f6bd170450c" // Amazon Linux 2 (us-east-1)
	domainName:   string & =~"^(([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\\.)+[a-zA-Z]{2,})$"

	// Database
	dbInstanceClass: string | *"db.t3.small"
	dbEngineVersion: string | *"15.3"

	// Feature flags
	enableLogging:    bool | *true
	enableMonitoring: bool | *true
	enableAurora:     bool | *false
	enableGoldenAmi:  bool | *false

	// Database
	dbFamily: string | *"postgres15"

	// Dynamic bucket generation
	storageBuckets: [...string] | *["assets", "uploads", "backups"]

	// Multi-AZ configuration
	availabilityZones: [...string] | *["us-east-1a", "us-east-1b"]
}

// ── Hidden helpers ──────────────────────────────────────────────────
_naming: {
	prefix: "\(variables.name)-\(variables.environment)"
	region: "us-east-1"
}

// ── Reusable definitions ────────────────────────────────────────────
#StandardTags: {
	app:       variables.name
	env:       variables.environment
	managedBy: "praxis"
	stack:     "end-to-end"
	...
}

#DataTags: #StandardTags & {tier: "data"}
#AppTags: #StandardTags & {tier: "app"}

#WebTags: #StandardTags & {tier: "web"}

#InfraTags: #StandardTags & {tier: "infra"}

// ═══════════════════════════════════════════════════════════════════════
// DATA SOURCES — query pre-existing infrastructure
// ═══════════════════════════════════════════════════════════════════════

data: {
	// Look up an existing shared-services VPC for peering.
	// Demonstrates data source resolution before DAG construction.
	sharedVpc: {
		kind: "VPC"
		filter: tag: {
			Name: "shared-services"
			env:  "shared"
		}
		region: _naming.region
	}
}

// ═══════════════════════════════════════════════════════════════════════
// RESOURCES
// ═══════════════════════════════════════════════════════════════════════

resources: {
	// ═══════════════════════════════════════════════════
	// 1. NETWORKING — VPC + Multi-AZ Subnets
	// ═══════════════════════════════════════════════════

	// Route 53 hosted zone for DNS records (cert validation + app A-record).
	hostedZone: {
		apiVersion: "praxis.io/v1"
		kind:       "Route53HostedZone"
		metadata: name: variables.domainName
		spec: {
			comment: "\(_naming.prefix) platform zone"
			tags:    #StandardTags
		}
	}

	vpc: {
		apiVersion: "praxis.io/v1"
		kind:       "VPC"
		metadata: name: "\(_naming.prefix)-vpc"
		spec: {
			region:             _naming.region
			cidrBlock:          "10.0.0.0/16"
			enableDnsHostnames: true
			enableDnsSupport:   true
			tags:               #StandardTags
		}
	}

	igw: {
		apiVersion: "praxis.io/v1"
		kind:       "InternetGateway"
		metadata: name: "\(_naming.prefix)-igw"
		spec: {
			region: _naming.region
			vpcId:  "${resources.vpc.outputs.vpcId}"
			tags:   #StandardTags
		}
	}

	// ── Public Subnets (web tier) ───────────────────────

	publicSubnetA: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(_naming.prefix)-public-a"
		spec: {
			region:              _naming.region
			vpcId:               "${resources.vpc.outputs.vpcId}"
			cidrBlock:           "10.0.1.0/24"
			availabilityZone:    variables.availabilityZones[0]
			mapPublicIpOnLaunch: true
			tags:                #WebTags
		}
	}

	publicSubnetB: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(_naming.prefix)-public-b"
		spec: {
			region:              _naming.region
			vpcId:               "${resources.vpc.outputs.vpcId}"
			cidrBlock:           "10.0.2.0/24"
			availabilityZone:    variables.availabilityZones[1]
			mapPublicIpOnLaunch: true
			tags:                #WebTags
		}
	}

	// ── App Subnets (application tier) ──────────────────

	appSubnetA: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(_naming.prefix)-app-a"
		spec: {
			region:           _naming.region
			vpcId:            "${resources.vpc.outputs.vpcId}"
			cidrBlock:        "10.0.10.0/24"
			availabilityZone: variables.availabilityZones[0]
			tags:             #AppTags
		}
	}

	appSubnetB: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(_naming.prefix)-app-b"
		spec: {
			region:           _naming.region
			vpcId:            "${resources.vpc.outputs.vpcId}"
			cidrBlock:        "10.0.11.0/24"
			availabilityZone: variables.availabilityZones[1]
			tags:             #AppTags
		}
	}

	// ── Data Subnets (database tier) ────────────────────

	dataSubnetA: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(_naming.prefix)-data-a"
		spec: {
			region:           _naming.region
			vpcId:            "${resources.vpc.outputs.vpcId}"
			cidrBlock:        "10.0.20.0/24"
			availabilityZone: variables.availabilityZones[0]
			tags:             #DataTags
		}
	}

	dataSubnetB: {
		apiVersion: "praxis.io/v1"
		kind:       "Subnet"
		metadata: name: "\(_naming.prefix)-data-b"
		spec: {
			region:           _naming.region
			vpcId:            "${resources.vpc.outputs.vpcId}"
			cidrBlock:        "10.0.21.0/24"
			availabilityZone: variables.availabilityZones[1]
			tags:             #DataTags
		}
	}

	// ── VPC Peering to Shared Services ──────────────────

	vpcPeering: {
		apiVersion: "praxis.io/v1"
		kind:       "VPCPeeringConnection"
		metadata: name: "\(_naming.prefix)-to-shared"
		spec: {
			region:         _naming.region
			requesterVpcId: "${resources.vpc.outputs.vpcId}"
			accepterVpcId:  "${data.sharedVpc.outputs.vpcId}"
			autoAccept:     true
			requesterOptions: allowDnsResolutionFromRemoteVpc: true
			accepterOptions: allowDnsResolutionFromRemoteVpc:  true
			tags: #InfraTags
		}
	}

	// ═══════════════════════════════════════════════════
	// 2. GATEWAYS & ROUTING
	// ═══════════════════════════════════════════════════

	natEip: {
		apiVersion: "praxis.io/v1"
		kind:       "ElasticIP"
		metadata: name: "\(_naming.prefix)-nat-eip"
		spec: {
			region: _naming.region
			domain: "vpc"
			tags:   #StandardTags
		}
	}

	natgw: {
		apiVersion: "praxis.io/v1"
		kind:       "NATGateway"
		metadata: name: "\(_naming.prefix)-natgw"
		spec: {
			region:           _naming.region
			subnetId:         "${resources.publicSubnetA.outputs.subnetId}"
			connectivityType: "public"
			allocationId:     "${resources.natEip.outputs.allocationId}"
			tags:             #StandardTags
		}
	}

	publicRT: {
		apiVersion: "praxis.io/v1"
		kind:       "RouteTable"
		metadata: name: "\(_naming.prefix)-public-rt"
		spec: {
			region: _naming.region
			vpcId:  "${resources.vpc.outputs.vpcId}"
			routes: [{
				destinationCidrBlock: "0.0.0.0/0"
				gatewayId:            "${resources.igw.outputs.internetGatewayId}"
			}]
			associations: [
				{subnetId: "${resources.publicSubnetA.outputs.subnetId}"},
				{subnetId: "${resources.publicSubnetB.outputs.subnetId}"},
			]
			tags: #WebTags
		}
	}

	appRT: {
		apiVersion: "praxis.io/v1"
		kind:       "RouteTable"
		metadata: name: "\(_naming.prefix)-app-rt"
		spec: {
			region: _naming.region
			vpcId:  "${resources.vpc.outputs.vpcId}"
			routes: [{
				destinationCidrBlock: "0.0.0.0/0"
				natGatewayId:         "${resources.natgw.outputs.natGatewayId}"
			}]
			associations: [
				{subnetId: "${resources.appSubnetA.outputs.subnetId}"},
				{subnetId: "${resources.appSubnetB.outputs.subnetId}"},
			]
			tags: #AppTags
		}
	}

	dataRT: {
		apiVersion: "praxis.io/v1"
		kind:       "RouteTable"
		metadata: name: "\(_naming.prefix)-data-rt"
		spec: {
			region: _naming.region
			vpcId:  "${resources.vpc.outputs.vpcId}"
			routes: [{
				destinationCidrBlock: "0.0.0.0/0"
				natGatewayId:         "${resources.natgw.outputs.natGatewayId}"
			}]
			associations: [
				{subnetId: "${resources.dataSubnetA.outputs.subnetId}"},
				{subnetId: "${resources.dataSubnetB.outputs.subnetId}"},
			]
			tags: #DataTags
		}
	}

	// ═══════════════════════════════════════════════════
	// 3. NETWORK ACLS — defense-in-depth
	// ═══════════════════════════════════════════════════

	publicNacl: {
		apiVersion: "praxis.io/v1"
		kind:       "NetworkACL"
		metadata: name: "\(_naming.prefix)-public-nacl"
		spec: {
			region: _naming.region
			vpcId:  "${resources.vpc.outputs.vpcId}"
			ingressRules: [
				{ruleNumber: 100, protocol: "tcp", ruleAction: "allow", cidrBlock: "0.0.0.0/0", fromPort: 80, toPort: 80},
				{ruleNumber: 110, protocol: "tcp", ruleAction: "allow", cidrBlock: "0.0.0.0/0", fromPort: 443, toPort: 443},
				{ruleNumber: 200, protocol: "tcp", ruleAction: "allow", cidrBlock: "0.0.0.0/0", fromPort: 1024, toPort: 65535},
			]
			egressRules: [
				{ruleNumber: 100, protocol: "-1", ruleAction: "allow", cidrBlock: "0.0.0.0/0"},
			]
			subnetAssociations: [
				"${resources.publicSubnetA.outputs.subnetId}",
				"${resources.publicSubnetB.outputs.subnetId}",
			]
			tags: #WebTags
		}
	}

	// ═══════════════════════════════════════════════════
	// 4. SECURITY GROUPS
	// ═══════════════════════════════════════════════════

	albSg: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: name: "\(_naming.prefix)-alb-sg"
		spec: {
			groupName:   "\(_naming.prefix)-alb-sg"
			description: "ALB — HTTP/HTTPS from internet"
			vpcId:       "${resources.vpc.outputs.vpcId}"
			ingressRules: [
				{protocol: "tcp", fromPort: 80, toPort: 80, cidrBlock: "0.0.0.0/0"},
				{protocol: "tcp", fromPort: 443, toPort: 443, cidrBlock: "0.0.0.0/0"},
			]
			egressRules: [{protocol: "-1", fromPort: 0, toPort: 0, cidrBlock: "0.0.0.0/0"}]
			tags: #WebTags
		}
	}

	appSg: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: name: "\(_naming.prefix)-app-sg"
		spec: {
			groupName:   "\(_naming.prefix)-app-sg"
			description: "App tier — port 8080 from VPC CIDR only"
			vpcId:       "${resources.vpc.outputs.vpcId}"
			ingressRules: [{
				protocol:  "tcp"
				fromPort:  8080
				toPort:    8080
				cidrBlock: "10.0.0.0/16"
			}]
			egressRules: [{protocol: "-1", fromPort: 0, toPort: 0, cidrBlock: "0.0.0.0/0"}]
			tags: #AppTags
		}
	}

	dataSg: {
		apiVersion: "praxis.io/v1"
		kind:       "SecurityGroup"
		metadata: name: "\(_naming.prefix)-data-sg"
		spec: {
			groupName:   "\(_naming.prefix)-data-sg"
			description: "Data tier — PostgreSQL from app subnets only"
			vpcId:       "${resources.vpc.outputs.vpcId}"
			ingressRules: [
				{protocol: "tcp", fromPort: 5432, toPort: 5432, cidrBlock: "10.0.10.0/24"},
				{protocol: "tcp", fromPort: 5432, toPort: 5432, cidrBlock: "10.0.11.0/24"},
			]
			egressRules: [{protocol: "-1", fromPort: 0, toPort: 0, cidrBlock: "0.0.0.0/0"}]
			tags: #DataTags
		}
	}

	// ═══════════════════════════════════════════════════
	// 5. TLS CERTIFICATE + DNS VALIDATION
	// ═══════════════════════════════════════════════════

	cert: {
		apiVersion: "praxis.io/v1"
		kind:       "ACMCertificate"
		metadata: name: "\(_naming.prefix)-cert"
		spec: {
			region:           _naming.region
			domainName:       variables.domainName
			validationMethod: "DNS"
			keyAlgorithm:     "RSA_2048"
			subjectAlternativeNames: [
				"*.\(variables.domainName)",
			]
			options: certificateTransparencyLoggingPreference: "ENABLED"
			tags: #StandardTags
		}
	}

	validationRecord: {
		apiVersion: "praxis.io/v1"
		kind:       "Route53Record"
		metadata: name: "\(_naming.prefix)-cert-validation"
		spec: {
			hostedZoneId: "${resources.hostedZone.outputs.hostedZoneId}"
			name:         "${resources.cert.outputs.dnsValidationRecords[0].resourceRecordName}"
			type:         "CNAME"
			ttl:          300
			resourceRecords: [
				"${resources.cert.outputs.dnsValidationRecords[0].resourceRecordValue}",
			]
		}
	}

	// ── DNS A-record pointing to ALB ────────────────────

	appDnsRecord: {
		apiVersion: "praxis.io/v1"
		kind:       "Route53Record"
		metadata: name: "\(_naming.prefix)-app-dns"
		spec: {
			hostedZoneId: "${resources.hostedZone.outputs.hostedZoneId}"
			name:         variables.domainName
			type:         "A"
			aliasTarget: {
				dnsName:              "${resources.alb.outputs.dnsName}"
				hostedZoneId:         "${resources.alb.outputs.canonicalHostedZoneId}"
				evaluateTargetHealth: true
			}
		}
	}

	// ── Route 53 Health Check ───────────────────────────

	albHealthCheck: {
		apiVersion: "praxis.io/v1"
		kind:       "Route53HealthCheck"
		metadata: name: "\(_naming.prefix)-alb-health"
		spec: {
			type:             "HTTPS"
			fqdn:             variables.domainName
			port:             443
			resourcePath:     "/healthz"
			requestInterval:  30
			failureThreshold: 3
			enableSNI:        true
			tags: #StandardTags & {purpose: "endpoint-health"}
		}
	}

	// ═══════════════════════════════════════════════════
	// 6. LOAD BALANCER
	// ═══════════════════════════════════════════════════

	alb: {
		apiVersion: "praxis.io/v1"
		kind:       "ALB"
		metadata: name: "\(_naming.prefix)-alb"
		spec: {
			region: _naming.region
			name:   "\(_naming.prefix)-alb"
			scheme: "internet-facing"
			subnets: [
				"${resources.publicSubnetA.outputs.subnetId}",
				"${resources.publicSubnetB.outputs.subnetId}",
			]
			securityGroups: ["${resources.albSg.outputs.groupId}"]
			tags: #WebTags
		}
	}

	targetGroup: {
		apiVersion: "praxis.io/v1"
		kind:       "TargetGroup"
		metadata: name: "\(_naming.prefix)-app-tg"
		spec: {
			region:     _naming.region
			port:       8080
			protocol:   "HTTP"
			vpcId:      "${resources.vpc.outputs.vpcId}"
			targetType: "instance"
			healthCheck: {
				path:               "/healthz"
				protocol:           "HTTP"
				port:               "8080"
				healthyThreshold:   3
				unhealthyThreshold: 3
				interval:           30
				timeout:            5
			}
			tags: #AppTags
		}
	}

	httpsListener: {
		apiVersion: "praxis.io/v1"
		kind:       "Listener"
		metadata: name: "\(_naming.prefix)-https"
		spec: {
			region:          _naming.region
			loadBalancerArn: "${resources.alb.outputs.loadBalancerArn}"
			port:            443
			protocol:        "HTTPS"
			certificateArn:  "${resources.cert.outputs.certificateArn}"
			defaultActions: [{
				type:           "forward"
				targetGroupArn: "${resources.targetGroup.outputs.targetGroupArn}"
			}]
			tags: #WebTags
		}
	}

	httpRedirectListener: {
		apiVersion: "praxis.io/v1"
		kind:       "Listener"
		metadata: name: "\(_naming.prefix)-http-redirect"
		spec: {
			region:          _naming.region
			loadBalancerArn: "${resources.alb.outputs.loadBalancerArn}"
			port:            80
			protocol:        "HTTP"
			defaultActions: [{
				type: "redirect"
				redirectConfig: {
					protocol:   "HTTPS"
					host:       "#{host}"
					port:       "443"
					path:       "/#{path}"
					query:      "#{query}"
					statusCode: "HTTP_301"
				}
			}]
			tags: #WebTags
		}
	}

	// ── Listener Rules — path-based routing ─────────────

	apiListenerRule: {
		apiVersion: "praxis.io/v1"
		kind:       "ListenerRule"
		metadata: name: "\(_naming.prefix)-api-rule"
		spec: {
			region:      _naming.region
			listenerArn: "${resources.httpsListener.outputs.listenerArn}"
			priority:    100
			conditions: [{
				field:  "path-pattern"
				values: ["/api/*"]
			}]
			actions: [{
				type:           "forward"
				targetGroupArn: "${resources.targetGroup.outputs.targetGroupArn}"
			}]
			tags: #WebTags
		}
	}

	healthListenerRule: {
		apiVersion: "praxis.io/v1"
		kind:       "ListenerRule"
		metadata: name: "\(_naming.prefix)-health-rule"
		spec: {
			region:      _naming.region
			listenerArn: "${resources.httpsListener.outputs.listenerArn}"
			priority:    1
			conditions: [{
				field:  "path-pattern"
				values: ["/healthz"]
			}]
			actions: [{
				type: "fixed-response"
				fixedResponseConfig: {
					statusCode:  "200"
					contentType: "text/plain"
					messageBody: "ok"
				}
			}]
			tags: #WebTags
		}
	}

	// ── NLB — internal TCP/gRPC service mesh ────────────

	nlb: {
		apiVersion: "praxis.io/v1"
		kind:       "NLB"
		metadata: name: "\(_naming.prefix)-nlb"
		spec: {
			region: _naming.region
			name:   "\(_naming.prefix)-nlb"
			scheme: "internal"
			subnets: [
				"${resources.appSubnetA.outputs.subnetId}",
				"${resources.appSubnetB.outputs.subnetId}",
			]
			crossZoneLoadBalancing: true
			tags: #AppTags
		}
	}

	grpcTargetGroup: {
		apiVersion: "praxis.io/v1"
		kind:       "TargetGroup"
		metadata: name: "\(_naming.prefix)-grpc-tg"
		spec: {
			region:     _naming.region
			port:       9090
			protocol:   "TCP"
			vpcId:      "${resources.vpc.outputs.vpcId}"
			targetType: "instance"
			healthCheck: {
				protocol:           "TCP"
				port:               "9090"
				healthyThreshold:   3
				unhealthyThreshold: 3
				interval:           30
			}
			tags: #AppTags
		}
	}

	nlbListener: {
		apiVersion: "praxis.io/v1"
		kind:       "Listener"
		metadata: name: "\(_naming.prefix)-nlb-grpc"
		spec: {
			region:          _naming.region
			loadBalancerArn: "${resources.nlb.outputs.loadBalancerArn}"
			port:            9090
			protocol:        "TCP"
			defaultActions: [{
				type:           "forward"
				targetGroupArn: "${resources.grpcTargetGroup.outputs.targetGroupArn}"
			}]
			tags: #AppTags
		}
	}

	// ═══════════════════════════════════════════════════
	// 7. COMPUTE — App Servers (for-loop comprehension)
	// ═══════════════════════════════════════════════════

	// Generate one app server per availability zone
	sshKeyPair: {
		apiVersion: "praxis.io/v1"
		kind:       "KeyPair"
		metadata: name: "\(_naming.prefix)-ssh"
		spec: {
			region:  _naming.region
			keyType: "ed25519"
			tags:    #AppTags
		}
	}

	for idx, az in variables.availabilityZones {
		let _az = az
		"appServer-\(idx)": {
			apiVersion: "praxis.io/v1"
			kind:       "EC2Instance"
			metadata: name: "\(_naming.prefix)-app-\(idx)"
			spec: {
				region:       _naming.region
				imageId:      variables.imageId
				instanceType: variables.instanceType
				subnetId:     "${resources.appSubnet\(["A", "B"][idx]).outputs.subnetId}"
				securityGroupIds: ["${resources.appSg.outputs.groupId}"]
				keyName:            "${resources.sshKeyPair.outputs.keyName}"
				iamInstanceProfile: "${resources.appInstanceProfile.outputs.instanceProfileName}"
				// Enable detailed monitoring in production
				monitoring: variables.environment == "prod"
				rootVolume: {
					sizeGiB:    30
					volumeType: "gp3"
					encrypted:  true
				}
				tags: #AppTags & {
					az:    _az
					index: "\(idx)"
				}
			}
		}

		// EBS data volume per app server
		"dataVolume-\(idx)": {
			apiVersion: "praxis.io/v1"
			kind:       "EBSVolume"
			metadata: name: "\(_naming.prefix)-data-vol-\(idx)"
			spec: {
				region:           _naming.region
				availabilityZone: _az
				volumeType:       "gp3"
				sizeGiB:          100
				encrypted:        true
				tags: #AppTags & {
					purpose: "app-data"
					az:      _az
					index:   "\(idx)"
				}
			}
		}
	}

	// ═══════════════════════════════════════════════════
	// 8. DATABASE — RDS PostgreSQL
	// ═══════════════════════════════════════════════════

	dbSubnetGroup: {
		apiVersion: "praxis.io/v1"
		kind:       "DBSubnetGroup"
		metadata: name: "\(_naming.prefix)-db-subnets"
		spec: {
			region:      _naming.region
			description: "Database subnet group for \(_naming.prefix)"
			subnetIds: [
				"${resources.dataSubnetA.outputs.subnetId}",
				"${resources.dataSubnetB.outputs.subnetId}",
			]
			tags: #DataTags
		}
	}

	database: {
		apiVersion: "praxis.io/v1"
		kind:       "RDSInstance"
		metadata: name: "\(_naming.prefix)-db"
		spec: {
			region:             _naming.region
			allocatedStorage:   100
			storageType:        "gp3"
			engine:             "postgres"
			engineVersion:      variables.dbEngineVersion
			instanceClass:      variables.dbInstanceClass
			masterUsername:     "admin"
			masterUserPassword: "ssm:///praxis/\(variables.environment)/db-password?sensitive=true"
			multiAZ:             variables.environment == "prod"
			dbSubnetGroupName:  "${resources.dbSubnetGroup.outputs.groupName}"
			parameterGroupName: "${resources.dbParamGroup.outputs.groupName}"
			vpcSecurityGroupIds: ["${resources.dataSg.outputs.groupId}"]
			tags: #DataTags
		}
		// Production databases are protected from accidental deletion
		// and cost-center tag changes are ignored during drift detection.
		lifecycle: {
			preventDestroy: variables.environment == "prod"
			ignoreChanges: ["tags.CostCenter"]
		}
	}

	dbParamGroup: {
		apiVersion: "praxis.io/v1"
		kind:       "DBParameterGroup"
		metadata: name: "\(_naming.prefix)-db-params"
		spec: {
			region:      _naming.region
			family:      variables.dbFamily
			description: "Custom PostgreSQL parameters for \(_naming.prefix)"
			parameters: {
				"shared_buffers":  "256MB"
				"max_connections":  "200"
				"log_statement":    "all"
				"log_min_duration_statement": "500"
			}
			tags: #DataTags
		}
	}

	// Conditional: Aurora cluster (alternative to standalone RDS)
	if variables.enableAurora {
		auroraCluster: {
			apiVersion: "praxis.io/v1"
			kind:       "AuroraCluster"
			metadata: name: "\(_naming.prefix)-aurora"
			spec: {
				region:                _naming.region
				engine:                "aurora-postgresql"
				engineVersion:         "15.4"
				masterUsername:        "admin"
				masterUserPassword:    "ssm:///praxis/\(variables.environment)/aurora-password?sensitive=true"
				databaseName:          variables.name
				port:                  5432
				dbSubnetGroupName:     "${resources.dbSubnetGroup.outputs.groupName}"
				vpcSecurityGroupIds: ["${resources.dataSg.outputs.groupId}"]
				storageEncrypted:      true
				backupRetentionPeriod: 14
				deletionProtection:    variables.environment == "prod"
				enabledCloudwatchLogsExports: ["postgresql"]
				tags: #DataTags
			}
			lifecycle: preventDestroy: variables.environment == "prod"
		}
	}

	// ═══════════════════════════════════════════════════
	// 9. IAM — App Execution Role + Policy
	// ═══════════════════════════════════════════════════

	appRole: {
		apiVersion: "praxis.io/v1"
		kind:       "IAMRole"
		metadata: name: "\(_naming.prefix)-app-role"
		spec: {
			path: "/app/"
			assumeRolePolicyDocument: json.Marshal({
				Version: "2012-10-17"
				Statement: [{
					Effect:    "Allow"
					Principal: Service: ["ec2.amazonaws.com", "lambda.amazonaws.com"]
					Action: "sts:AssumeRole"
				}]
			})
			tags: #AppTags
		}
	}

	appPolicy: {
		apiVersion: "praxis.io/v1"
		kind:       "IAMPolicy"
		metadata: name: "\(_naming.prefix)-app-policy"
		spec: {
			path: "/app/"
			policyDocument: json.Marshal({
				Version: "2012-10-17"
				Statement: [
					{
						Sid:    "S3Access"
						Effect: "Allow"
						Action: [
							"s3:GetObject",
							"s3:PutObject",
							"s3:ListBucket",
						]
						Resource: "*"
					},
					{
						Sid:    "SQSAccess"
						Effect: "Allow"
						Action: [
							"sqs:SendMessage",
							"sqs:ReceiveMessage",
							"sqs:DeleteMessage",
						]
						Resource: "arn:aws:sqs:\(_naming.region):*:\(_naming.prefix)-dlq"
					},
					{
						Sid:    "SNSPublish"
						Effect: "Allow"
						Action: ["sns:Publish"]
						Resource: "arn:aws:sns:\(_naming.region):*:\(_naming.prefix)-alerts"
					},
					{
						Sid:    "CloudWatchLogs"
						Effect: "Allow"
						Action: [
							"logs:CreateLogStream",
							"logs:PutLogEvents",
						]
						Resource: "arn:aws:logs:\(_naming.region):*:log-group:/praxis/\(_naming.prefix)/app:*"
					},
				]
			})
			tags: #AppTags
		}
	}

	appInstanceProfile: {
		apiVersion: "praxis.io/v1"
		kind:       "IAMInstanceProfile"
		metadata: name: "\(_naming.prefix)-app-profile"
		spec: {
			path:     "/app/"
			roleName: "${resources.appRole.outputs.roleName}"
			tags:     #AppTags
		}
	}

	opsGroup: {
		apiVersion: "praxis.io/v1"
		kind:       "IAMGroup"
		metadata: name: "\(_naming.prefix)-ops"
		spec: {
			path: "/ops/"
			managedPolicyArns: [
				"arn:aws:iam::aws:policy/ReadOnlyAccess",
			]
		}
	}

	ciUser: {
		apiVersion: "praxis.io/v1"
		kind:       "IAMUser"
		metadata: name: "\(_naming.prefix)-ci"
		spec: {
			path: "/ci/"
			groups: ["${resources.opsGroup.outputs.groupName}"]
			managedPolicyArns: [
				"${resources.appPolicy.outputs.arn}",
			]
			tags: #AppTags & {purpose: "ci-cd"}
		}
	}

	// ═══════════════════════════════════════════════════
	// 10. LAMBDA — Async Worker
	// ═══════════════════════════════════════════════════

	worker: {
		apiVersion: "praxis.io/v1"
		kind:       "LambdaFunction"
		metadata: name: "\(_naming.prefix)-worker"
		spec: {
			region:  _naming.region
			runtime: "provided.al2023"
			handler: "bootstrap"
			role:    "${resources.appRole.outputs.arn}"
			code: zipFile: "bootstrap-placeholder"
			memorySize:   256
			timeout:      60
			environment: {
				DB_HOST:     "${resources.database.outputs.endpoint}"
				QUEUE_URL:   "${resources.dlq.outputs.queueUrl}"
				LOG_GROUP:   "${resources.appLogGroup.outputs.logGroupName}"
				ALERT_TOPIC: "${resources.alertTopic.outputs.topicArn}"
			}
			vpcConfig: {
				subnetIds: [
					"${resources.appSubnetA.outputs.subnetId}",
					"${resources.appSubnetB.outputs.subnetId}",
				]
				securityGroupIds: ["${resources.appSg.outputs.groupId}"]
			}
			tags: #AppTags & {purpose: "async-worker"}
		}
	}

	sharedLayer: {
		apiVersion: "praxis.io/v1"
		kind:       "LambdaLayer"
		metadata: name: "\(_naming.prefix)-shared-layer"
		spec: {
			region:      _naming.region
			description: "Shared utilities for \(_naming.prefix) workers"
			compatibleRuntimes: ["provided.al2023"]
			compatibleArchitectures: ["x86_64", "arm64"]
			code: s3: {
				bucket: "${resources.bucket-assets.outputs.bucketName}"
				key:    "layers/shared-layer.zip"
			}
		}
	}

	workerSnsPermission: {
		apiVersion: "praxis.io/v1"
		kind:       "LambdaPermission"
		metadata: name: "\(_naming.prefix)-worker-sns-perm"
		spec: {
			region:       _naming.region
			functionName: "${resources.worker.outputs.functionName}"
			action:       "lambda:InvokeFunction"
			principal:    "sns.amazonaws.com"
			sourceArn:    "${resources.alertTopic.outputs.topicArn}"
		}
	}

	workerDlqTrigger: {
		apiVersion: "praxis.io/v1"
		kind:       "EventSourceMapping"
		metadata: name: "\(_naming.prefix)-worker-sqs-trigger"
		spec: {
			region:                           _naming.region
			functionName:                     "${resources.worker.outputs.functionName}"
			eventSourceArn:                   "${resources.dlq.outputs.queueArn}"
			batchSize:                        10
			maximumBatchingWindowInSeconds:    60
			enabled:                          true
			functionResponseTypes: ["ReportBatchItemFailures"]
		}
	}

	// ═══════════════════════════════════════════════════
	// 11. MESSAGING — SNS + SQS Dead-Letter Queue
	// ═══════════════════════════════════════════════════

	alertTopic: {
		apiVersion: "praxis.io/v1"
		kind:       "SNSTopic"
		metadata: name: "\(_naming.prefix)-alerts"
		spec: {
			region:    _naming.region
			topicName: "\(_naming.prefix)-alerts"
			tags: #StandardTags & {purpose: "alerting"}
		}
	}

	dlq: {
		apiVersion: "praxis.io/v1"
		kind:       "SQSQueue"
		metadata: name: "\(_naming.prefix)-dlq"
		spec: {
			region:                   _naming.region
			queueName:              "\(_naming.prefix)-dlq"
			messageRetentionPeriod: 1209600 // 14 days
			visibilityTimeout:      300
			tags: #StandardTags & {purpose: "dead-letter-queue"}
		}
	}

	// SNS → SQS subscription (alerts fan-out to DLQ for processing)
	alertToQueueSub: {
		apiVersion: "praxis.io/v1"
		kind:       "SNSSubscription"
		metadata: name: "\(_naming.prefix)-alert-to-dlq"
		spec: {
			region:             _naming.region
			topicArn:           "${resources.alertTopic.outputs.topicArn}"
			protocol:           "sqs"
			endpoint:           "${resources.dlq.outputs.queueArn}"
			rawMessageDelivery: true
		}
	}

	// SQS queue policy — allow SNS to publish to the DLQ
	dlqPolicy: {
		apiVersion: "praxis.io/v1"
		kind:       "SQSQueuePolicy"
		metadata: name: "\(_naming.prefix)-dlq-policy"
		spec: {
			region:    _naming.region
			queueName: "${resources.dlq.outputs.queueName}"
			policy: {
				Version: "2012-10-17"
				Statement: [{
					Sid:       "AllowSNSPublish"
					Effect:    "Allow"
					Principal: Service: "sns.amazonaws.com"
					Action:   "sqs:SendMessage"
					Resource: "${resources.dlq.outputs.queueArn}"
					Condition: ArnEquals: "aws:SourceArn": "${resources.alertTopic.outputs.topicArn}"
				}]
			}
		}
	}

	// ═══════════════════════════════════════════════════
	// 12. OBSERVABILITY — CloudWatch Logs + Alarm
	// ═══════════════════════════════════════════════════

	appLogGroup: {
		apiVersion: "praxis.io/v1"
		kind:       "LogGroup"
		metadata: name: "/praxis/\(_naming.prefix)/app"
		spec: {
			region:          _naming.region
			retentionInDays: 90
			tags: #AppTags & {purpose: "application-logs"}
		}
	}

	// Conditional: CloudWatch alarm only when monitoring is enabled
	if variables.enableMonitoring {
		highErrorAlarm: {
			apiVersion: "praxis.io/v1"
			kind:       "MetricAlarm"
			metadata: name: "\(_naming.prefix)-high-5xx-errors"
			spec: {
				region:             _naming.region
				alarmDescription:   "ALB 5xx error rate exceeds threshold"
				namespace:          "AWS/ApplicationELB"
				metricName:         "HTTPCode_ELB_5XX_Count"
				statistic:          "Sum"
				period:             300
				evaluationPeriods:  2
				threshold:          50
				comparisonOperator: "GreaterThanThreshold"
				alarmActions: ["${resources.alertTopic.outputs.topicArn}"]
				dimensions: LoadBalancer: "${resources.alb.outputs.loadBalancerArn}"
				tags: #StandardTags & {purpose: "monitoring"}
			}
		}
	}

	// Conditional: CloudWatch dashboard for platform overview
	if variables.enableMonitoring {
		platformDashboard: {
			apiVersion: "praxis.io/v1"
			kind:       "Dashboard"
			metadata: name: "\(_naming.prefix)-dashboard"
			spec: {
				region: _naming.region
				dashboardBody: json.Marshal({
					widgets: [
						{
							type: "metric"
							x: 0, y: 0, width: 12, height: 6
							properties: {
								metrics: [
									["AWS/ApplicationELB", "RequestCount", "LoadBalancer", "\(_naming.prefix)-alb"],
									["AWS/ApplicationELB", "HTTPCode_ELB_5XX_Count", "LoadBalancer", "\(_naming.prefix)-alb"],
								]
								period: 300
								stat:   "Sum"
								region: _naming.region
								title:  "ALB Traffic & Errors"
							}
						},
						{
							type: "metric"
							x: 12, y: 0, width: 12, height: 6
							properties: {
								metrics: [
									["AWS/RDS", "CPUUtilization", "DBInstanceIdentifier", "\(_naming.prefix)-db"],
									["AWS/RDS", "FreeableMemory", "DBInstanceIdentifier", "\(_naming.prefix)-db"],
								]
								period: 300
								stat:   "Average"
								region: _naming.region
								title:  "RDS Performance"
							}
						},
						{
							type: "metric"
							x: 0, y: 6, width: 12, height: 6
							properties: {
								metrics: [
									["AWS/Lambda", "Invocations", "FunctionName", "\(_naming.prefix)-worker"],
									["AWS/Lambda", "Errors", "FunctionName", "\(_naming.prefix)-worker"],
								]
								period: 300
								stat:   "Sum"
								region: _naming.region
								title:  "Lambda Worker"
							}
						},
					]
				})
			}
		}
	}

	// ═══════════════════════════════════════════════════
	// 13. STORAGE — Dynamic S3 Buckets (for-loop)
	// ═══════════════════════════════════════════════════

	// Generate one S3 bucket per entry in storageBuckets
	for _, suffix in variables.storageBuckets {
		"bucket-\(suffix)": {
			apiVersion: "praxis.io/v1"
			kind:       "S3Bucket"
			metadata: name: "\(_naming.prefix)-\(suffix)"
			spec: {
				region:     _naming.region
				versioning: true
				acl:        "private"
				encryption: {
					enabled:   true
					algorithm: "AES256"
				}
				tags: #StandardTags & {
					purpose: suffix
				}
			}
			lifecycle: preventDestroy: variables.environment == "prod"
		}
	}

	// Conditional: log-aggregator bucket (only when logging is enabled)
	if variables.enableLogging {
		"log-aggregator": {
			apiVersion: "praxis.io/v1"
			kind:       "S3Bucket"
			metadata: name: "\(_naming.prefix)-logs"
			spec: {
				region:     _naming.region
				versioning: false
				acl:        "private"
				encryption: {
					enabled:   true
					algorithm: "AES256"
				}
				tags: #StandardTags & {
					purpose: "log-aggregation"
				}
			}
		}
	}

	// ═══════════════════════════════════════════════════
	// 14. CONTAINER REGISTRY — ECR
	// ═══════════════════════════════════════════════════

	appRegistry: {
		apiVersion: "praxis.io/v1"
		kind:       "ECRRepository"
		metadata: name: "\(_naming.prefix)-app"
		spec: {
			region:             _naming.region
			imageTagMutability: "IMMUTABLE"
			imageScanningConfiguration: scanOnPush: true
			encryptionConfiguration: encryptionType: "AES256"
			tags: #AppTags
		}
	}

	registryLifecycle: {
		apiVersion: "praxis.io/v1"
		kind:       "ECRLifecyclePolicy"
		metadata: name: "\(_naming.prefix)-app-lifecycle"
		spec: {
			region:         _naming.region
			repositoryName: "${resources.appRegistry.outputs.repositoryName}"
			lifecyclePolicyText: json.Marshal({
				rules: [{
					rulePriority: 1
					description:  "Expire untagged images older than 14 days"
					selection: {
						tagStatus:   "untagged"
						countType:   "sinceImagePushed"
						countUnit:   "days"
						countNumber: 14
					}
					action: type: "expire"
				}, {
					rulePriority: 2
					description:  "Keep only last 50 tagged images"
					selection: {
						tagStatus:     "tagged"
						tagPrefixList: ["v"]
						countType:     "imageCountMoreThan"
						countNumber:   50
					}
					action: type: "expire"
				}]
			})
		}
	}

	// ═══════════════════════════════════════════════════
	// 15. GOLDEN AMI (conditional)
	// ═══════════════════════════════════════════════════

	// Conditional: create a regional AMI copy for the platform
	if variables.enableGoldenAmi {
		goldenAmi: {
			apiVersion: "praxis.io/v1"
			kind:       "AMI"
			metadata: name: "\(_naming.prefix)-golden"
			spec: {
				region:      _naming.region
				description: "Golden AMI for \(_naming.prefix) app servers"
				source: fromAMI: {
					sourceImageId: variables.imageId
					encrypted:     true
				}
				tags: #AppTags & {purpose: "golden-ami"}
			}
		}
	}
}
