import "encoding/json"

// saas-platform.cue — Production-grade SaaS platform infrastructure.
//
// A comprehensive demo showcasing the full power of Praxis: declarative
// infrastructure-as-code with dependency graphs, for-loop comprehensions,
// conditional resources, data sources, lifecycle protection, reusable
// definitions, and policy-compatible tagging — all in a single template.
//
// ┌───────────────────────────────────────────────────────────────────┐
// │                        ARCHITECTURE                               │
// │                                                                   │
// │  VPC (10.0.0.0/16) — Multi-AZ, DNS-enabled                        │
// │  ├── Internet Gateway                                             │
// │  ├── Public Subnets (AZ-a, AZ-b)                                  │
// │  │   ├── Route Table → IGW                                        │
// │  │   ├── NAT Gateway + Elastic IP                                 │
// │  │   └── ALB (HTTPS) → Target Group                               │
// │  ├── App Subnets (AZ-a, AZ-b)                                     │
// │  │   ├── Route Table → NAT                                        │
// │  │   ├── App Security Group (8080 from ALB SG)                    │
// │  │   ├── App Servers (one per AZ, via for-loop)                   │
// │  │   └── Lambda (async worker)                                    │
// │  ├── Data Subnets (AZ-a, AZ-b)                                    │
// │  │   ├── Route Table → NAT                                        │
// │  │   ├── DB Subnet Group                                          │
// │  │   ├── Data Security Group (5432 from App SG CIDR)              │
// │  │   └── RDS PostgreSQL (Multi-AZ in prod)                        │
// │  ├── Network ACL (defense-in-depth on public subnets)             │
// │  ├── ACM Certificate + DNS Validation Record                      │
// │  ├── SNS Alert Topic + SQS Dead-Letter Queue                      │
// │  ├── CloudWatch Log Group + Metric Alarm                          │
// │  ├── IAM Role + Policy (app execution role)                       │
// │  └── S3 Buckets (dynamic from list: assets, uploads, backups)     │
// │      └── Conditional: S3 log-aggregator bucket                    │
// └───────────────────────────────────────────────────────────────────┘
//
// DAG (abridged):
//   hostedZone → validationRecord, appDnsRecord
//   vpc → igw, subnets (×6), nacl, securityGroups
//   publicSubnets → natEip → natgw → appRT, dataRT
//   igw → publicRT
//   albSg → alb → targetGroup → httpsListener ← cert
//   cert → validationRecord
//   appSg → appServers (for-loop), lambda
//   dataSg → dbSubnetGroup → database
//   snsAlertTopic → sqsDlq, alarm
//   iamRole → iamPolicy → lambda
//
// Features demonstrated:
//   ✓ Variables — typed, constrained (regex, enum, defaults, lists, bools)
//   ✓ For-loop comprehensions — app servers + S3 buckets from lists
//   ✓ Conditional resources — log bucket, monitoring toggle, NAT in prod
//   ✓ CUE definitions (#) — reusable tag blocks
//   ✓ Hidden helpers (_) — naming prefix
//   ✓ Data sources — see examples/stacks/data-source-*.cue for data source demos
//   ✓ Output expressions — full DAG wiring across 30+ resources
//   ✓ Lifecycle rules — preventDestroy on database + ignoreChanges
//   ✓ Environment-aware logic — Multi-AZ RDS, monitoring, instance sizing
//   ✓ 15+ resource kinds — VPC, Subnet, IGW, NAT, EIP, RT, SG, NACL,
//     EC2, S3, RDS, ALB, TG, Listener, ACM, DNS, Lambda, SNS, SQS,
//     CloudWatch, IAM Role, IAM Policy
//
// Usage:
//   praxis template register examples/stacks/saas-platform.cue \
//     --description "Production SaaS platform — multi-AZ, ALB, RDS, Lambda, monitoring"
//
//   # Dry-run to preview the execution plan
//   praxis deploy saas-platform --account local \
//     -f examples/stacks/saas-platform.vars.json --dry-run
//
//   # Deploy
//   praxis deploy saas-platform --account local \
//     -f examples/stacks/saas-platform.vars.json --key acme-prod --wait

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
	stack:     "saas-platform"
	...
}

#DataTags: #StandardTags & {tier: "data"}
#AppTags: #StandardTags & {tier: "app"}

#WebTags: #StandardTags & {tier: "web"}

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

	// ═══════════════════════════════════════════════════
	// 7. COMPUTE — App Servers (for-loop comprehension)
	// ═══════════════════════════════════════════════════

	// Generate one app server per availability zone
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
			region:            _naming.region
			allocatedStorage:  100
			storageType:       "gp3"
			engine:            "postgres"
			engineVersion:     variables.dbEngineVersion
			instanceClass:     variables.dbInstanceClass
			masterUsername:    "admin"
			multiAZ:             variables.environment == "prod"
			dbSubnetGroupName: "${resources.dbSubnetGroup.outputs.groupName}"
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
			role:    "${resources.appRole.outputs.roleArn}"
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
}
